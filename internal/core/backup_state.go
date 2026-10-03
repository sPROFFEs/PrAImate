package core

// Backup state export — the 1.1 structure moved the source of truth for
// chats, agents, memory, MCP, schedules and watchers into the SQLite DB
// under the PrAImate data root, which the workspaces-root backup repo
// doesn't see.
// ExportBackupState writes a consistent snapshot of that state into a
// subdir of the backup repo so the existing git-based backup commits it
// alongside the on-disk chat sandboxes.
//
// Layout under <repoDir>/.praimate-state/:
//   db.sqlite        encrypted VACUUM INTO snapshot of the live DB
//   db.sqlite.key    password-protected portable key envelope
//   agents/<id>.yaml exported agent definitions (portable, human-readable)
//
// ImportBackupState is the other direction: after a clone/pull brings a
// remote machine's snapshot into the repo, it row-merges that snapshot
// into the live DB (newer-updated_at wins for keyed rows; append-only
// tables dedupe on a natural key). Together the pair gives multi-host
// sharing: git moves the snapshot, the importer merges it.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BackupStateDir is the conventional subdir name inside the backup repo.
const BackupStateDir = ".praimate-state"

// ExportBackupState snapshots the DB and exports all agents into
// <repoDir>/.praimate-state/. No-op (nil) when Core has no store.
func (c *Core) ExportBackupState(ctx context.Context, repoDir string) error {
	if c.store == nil {
		return nil
	}
	stateDir := filepath.Join(repoDir, BackupStateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("export backup state: mkdir: %w", err)
	}

	// 1. Consistent DB snapshot (WAL-safe via VACUUM INTO). Snapshot to
	// a sidecar first and only replace the committed file when the bytes
	// actually changed — otherwise every sync would commit a fresh
	// binary blob and two idle machines would ping-pong commits forever.
	dbDest := filepath.Join(stateDir, "db.sqlite")
	dbTmp := dbDest + ".tmp"
	if err := c.store.Snapshot(ctx, dbTmp); err != nil {
		return fmt.Errorf("export backup state: db snapshot: %w", err)
	}
	if same, _ := filesEqual(dbDest, dbTmp); same {
		_ = os.Remove(dbTmp)
	} else if err := replaceFile(dbTmp, dbDest); err != nil {
		return fmt.Errorf("export backup state: replace snapshot: %w", err)
	}
	envelopeDest := dbDest + ".key"
	envelopeTmp := envelopeDest + ".tmp"
	if err := copyPrivateFile(c.store.EncryptionKeyPath(), envelopeTmp); err != nil {
		return fmt.Errorf("export backup state: key envelope: %w", err)
	}
	if same, _ := filesEqual(envelopeDest, envelopeTmp); same {
		_ = os.Remove(envelopeTmp)
	} else if err := replaceFile(envelopeTmp, envelopeDest); err != nil {
		return fmt.Errorf("export backup state: replace key envelope: %w", err)
	}

	// 2. Agents as portable YAML. Rewritten each time; stale files for
	// deleted agents are pruned so the export mirrors the DB exactly.
	agentsDir := filepath.Join(stateDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return fmt.Errorf("export backup state: mkdir agents: %w", err)
	}
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return fmt.Errorf("export backup state: list agents: %w", err)
	}
	keep := map[string]bool{}
	for i := range agents {
		a := &agents[i]
		keep[a.ID+".yaml"] = true
		body, err := MarshalAgentYAML(a)
		if err != nil {
			return fmt.Errorf("export backup state: marshal %s: %w", a.ID, err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, a.ID+".yaml"), body, 0o644); err != nil {
			return fmt.Errorf("export backup state: write %s: %w", a.ID, err)
		}
	}
	if entries, err := os.ReadDir(agentsDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() && !keep[e.Name()] {
				_ = os.Remove(filepath.Join(agentsDir, e.Name()))
			}
		}
	}
	return nil
}

// mergeTableSpec describes how one table's rows from a remote snapshot
// merge into the live DB. Two strategies:
//   - updatedAt set: keyed upsert — insert when the key is absent,
//     overwrite when the remote row's updated_at is strictly newer.
//   - updatedAt empty: append-only — insert when the natural key is
//     absent, never touch existing rows.
//
// skipCols lists columns that must not travel across hosts (sqlite
// AUTOINCREMENT ids would collide).
//
// Deliberately absent: schedules and watchers (they reference
// host-local filesystem paths), and any deletion propagation — a row
// deleted on one host can reappear after importing another host's
// snapshot. Tombstones are a follow-up if that bites.
type mergeTableSpec struct {
	name      string
	key       []string
	updatedAt string
	skipCols  []string
}

// mergeTables lists the synced tables in FK-safe insert order.
var mergeTables = []mergeTableSpec{
	{name: "agents", key: []string{"id"}, updatedAt: "updated_at"},
	{name: "chats", key: []string{"id"}, updatedAt: "updated_at"},
	{name: "messages", key: []string{"chat_id", "ts", "role", "content"}, skipCols: []string{"id"}},
	{name: "mcp_servers", key: []string{"id"}},
	{name: "settings_cli", key: []string{"key"}, updatedAt: "updated_at"},
	{name: "settings_gui", key: []string{"key"}, updatedAt: "updated_at"},
}

// ImportBackupState row-merges the snapshot at
// <repoDir>/.praimate-state/db.sqlite into the live DB. Missing
// snapshot = nil (nothing to import — e.g. the remote predates this
// feature). Per-row failures (FK violations on chats deleted locally,
// schema drift) are tolerated and counted; the import keeps going so
// one bad row can't block a whole machine's chats from arriving.
func (c *Core) ImportBackupState(ctx context.Context, repoDir string) error {
	return c.importBackupState(ctx, repoDir, "", false)
}

// ImportBackupStateWithPassword unlocks a remote whose envelope uses a different
// password. It preserves the local database credentials and existing rows.
func (c *Core) ImportBackupStateWithPassword(ctx context.Context, repoDir, password string) error {
	return c.importBackupState(ctx, repoDir, password, true)
}

// ValidateBackupState checks that an existing snapshot can be decrypted without
// merging its rows. Export preflight must not resurrect locally deleted chats
// from the previous snapshot or revert local changes before creating a new one.
func (c *Core) ValidateBackupState(ctx context.Context, repoDir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	snap, _, err := c.openBackupState(repoDir, "", false)
	if err != nil {
		return err
	}
	if snap != nil {
		return snap.Close()
	}
	return nil
}

func (c *Core) openBackupState(repoDir, password string, explicit bool) (*sql.DB, bool, error) {
	if c.store == nil {
		return nil, false, nil
	}
	snapPath := filepath.Join(repoDir, BackupStateDir, "db.sqlite")
	if _, err := os.Stat(snapPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("import backup state: inspect snapshot: %w", err)
	}
	var snap *sql.DB
	var legacyPlaintext bool
	var err error
	if explicit {
		snap, legacyPlaintext, err = c.store.OpenSnapshotWithPassword(snapPath, snapPath+".key", password)
	} else {
		snap, legacyPlaintext, err = c.store.OpenSnapshot(snapPath, snapPath+".key")
	}
	if err != nil {
		return nil, false, fmt.Errorf("import backup state: open snapshot: %w", err)
	}
	return snap, legacyPlaintext, nil
}

func (c *Core) importBackupState(ctx context.Context, repoDir, password string, explicit bool) error {
	snap, legacyPlaintext, err := c.openBackupState(repoDir, password, explicit)
	if err != nil || snap == nil {
		return err
	}
	defer snap.Close()

	live := c.store.DB()
	var firstErr error
	for _, spec := range mergeTables {
		if err := mergeTable(ctx, live, snap, spec, legacyPlaintext); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("import backup state: %s: %w", spec.name, err)
		}
	}
	return firstErr
}

func portableSQLiteURI(path string) string {
	pathURL := &url.URL{Path: filepath.ToSlash(path)}
	return "file:" + pathURL.EscapedPath()
}

// mergeTable applies one table's merge strategy. Columns are read from
// the snapshot and intersected with the live table's columns, so a
// snapshot written by a newer or older PrAImate version degrades to the
// shared column set instead of failing.
func mergeTable(ctx context.Context, live, snap *sql.DB, spec mergeTableSpec, sanitizeLegacy bool) error {
	snapCols, err := tableColumns(ctx, snap, spec.name)
	if err != nil {
		return nil // table absent in snapshot — older remote version
	}
	liveCols, err := tableColumns(ctx, live, spec.name)
	if err != nil {
		return fmt.Errorf("live table columns: %w", err)
	}
	skip := map[string]bool{}
	for _, c := range spec.skipCols {
		skip[c] = true
	}
	var cols []string
	for col := range snapCols {
		if liveCols[col] && !skip[col] {
			cols = append(cols, col)
		}
	}
	sort.Strings(cols)
	for _, k := range spec.key {
		if !containsStr(cols, k) {
			return nil // key column missing — can't merge safely
		}
	}

	rows, err := snap.QueryContext(ctx, "SELECT "+quoteCols(cols)+" FROM "+spec.name)
	if err != nil {
		return fmt.Errorf("read snapshot rows: %w", err)
	}
	defer rows.Close()

	insertSQL := "INSERT INTO " + spec.name + " (" + quoteCols(cols) + ") VALUES (" +
		strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")"
	whereSQL, keyIdx := buildKeyWhere(cols, spec.key)

	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("scan snapshot row: %w", err)
		}
		if sanitizeLegacy && !sanitizeImportedRow(spec.name, cols, vals) {
			continue
		}
		keyVals := make([]any, len(keyIdx))
		for i, idx := range keyIdx {
			keyVals[i] = vals[idx]
		}

		if spec.updatedAt == "" {
			var one int
			err := live.QueryRowContext(ctx, "SELECT 1 FROM "+spec.name+" WHERE "+whereSQL, keyVals...).Scan(&one)
			if err == sql.ErrNoRows {
				_, _ = live.ExecContext(ctx, insertSQL, vals...) // per-row best-effort
			}
			continue
		}

		var localUpdated sql.NullString
		err := live.QueryRowContext(ctx,
			"SELECT "+quoteCol(spec.updatedAt)+" FROM "+spec.name+" WHERE "+whereSQL, keyVals...).Scan(&localUpdated)
		switch {
		case err == sql.ErrNoRows:
			_, _ = live.ExecContext(ctx, insertSQL, vals...)
		case err != nil:
			return fmt.Errorf("probe local row: %w", err)
		default:
			remoteUpdated, _ := vals[indexOf(cols, spec.updatedAt)].(string)
			// RFC3339 timestamps compare correctly as strings.
			if remoteUpdated > localUpdated.String {
				setSQL, setVals := buildUpdateSet(cols, keyIdx, vals)
				if setSQL != "" {
					_, _ = live.ExecContext(ctx,
						"UPDATE "+spec.name+" SET "+setSQL+" WHERE "+whereSQL,
						append(setVals, keyVals...)...)
				}
			}
		}
	}
	return rows.Err()
}

func copyPrivateFile(src, dest string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, body, 0o600); err != nil {
		return err
	}
	return nil
}

func replaceFile(src, dest string) error {
	old := dest + ".replacing"
	_ = os.Remove(old)
	if _, err := os.Stat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			return err
		}
	}
	if err := os.Rename(src, dest); err != nil {
		_ = os.Rename(old, dest)
		return err
	}
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// sanitizeImportedRow enforces the same credential boundary on old backup
// repositories created before exports were scrubbed.
func sanitizeImportedRow(table string, cols []string, vals []any) bool {
	switch table {
	case "settings_cli":
		if idx := indexOf(cols, "key"); idx >= 0 && stringValue(vals[idx]) == "local_llm.api_key" {
			return false
		}
	case "mcp_servers":
		for _, col := range []string{"env_json", "auth_json"} {
			if idx := indexOf(cols, col); idx >= 0 {
				vals[idx] = "{}"
			}
		}
	case "chats":
		idx := indexOf(cols, "settings_json")
		if idx < 0 {
			break
		}
		var settings map[string]any
		if json.Unmarshal([]byte(stringValue(vals[idx])), &settings) != nil {
			break
		}
		if local, _ := settings["local"].(map[string]any); local != nil {
			delete(local, "api_key")
			if body, err := json.Marshal(settings); err == nil {
				vals[idx] = string(body)
			}
		}
	}
	return true
}

func stringValue(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return ""
	}
}

// tableColumns returns the column set of a table (error when the table
// doesn't exist).
func tableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("table %s not found", table)
	}
	return cols, rows.Err()
}

func quoteCol(c string) string { return `"` + c + `"` }
func quoteCols(cs []string) string {
	q := make([]string, len(cs))
	for i, c := range cs {
		q[i] = quoteCol(c)
	}
	return strings.Join(q, ", ")
}

func containsStr(ss []string, want string) bool { return indexOf(ss, want) >= 0 }

func indexOf(ss []string, want string) int {
	for i, s := range ss {
		if s == want {
			return i
		}
	}
	return -1
}

// buildKeyWhere returns "k1 = ? AND k2 = ?" plus the positions of the
// key columns inside cols.
func buildKeyWhere(cols, key []string) (string, []int) {
	parts := make([]string, len(key))
	idx := make([]int, len(key))
	for i, k := range key {
		parts[i] = quoteCol(k) + " = ?"
		idx[i] = indexOf(cols, k)
	}
	return strings.Join(parts, " AND "), idx
}

// buildUpdateSet returns "c1 = ?, c2 = ?" for every non-key column and
// the matching values.
func buildUpdateSet(cols []string, keyIdx []int, vals []any) (string, []any) {
	isKey := map[int]bool{}
	for _, i := range keyIdx {
		isKey[i] = true
	}
	var parts []string
	var out []any
	for i, c := range cols {
		if isKey[i] {
			continue
		}
		parts = append(parts, quoteCol(c)+" = ?")
		out = append(out, vals[i])
	}
	return strings.Join(parts, ", "), out
}

// filesEqual reports whether two files have identical contents. Either
// file missing = not equal.
func filesEqual(a, b string) (bool, error) {
	ra, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	rb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ra, rb), nil
}
