// Package store owns the single PrAImate SQLite database. It is the
// only package in the tree that opens the DB file. Everything else —
// internal/core and the GUI — talks to a *Store via typed query
// methods.
//
// Migrations live under migrations/ and are embedded into the binary.
// They run in lexicographic order on Open(); each file is wrapped in a
// transaction and recorded in schema_version.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/vfs/xts"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the encrypted SQLite connection pool.
type Store struct {
	db           *sql.DB
	path         string
	keyPath      string
	key          []byte
	credentialMu sync.RWMutex
	password     []byte
}

// openWithKey opens (or creates) the database with an already-unwrapped key.
// Ownership of key and password transfers to the Store and both buffers are
// cleared on Close.
func openWithKey(path string, key, password []byte) (*Store, error) {
	if path == "" {
		zeroBytes(key)
		zeroBytes(password)
		return nil, errors.New("store: empty path")
	}
	if len(key) != encryptionKeyBytes {
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: database key has %d bytes; expected %d", len(key), encryptionKeyBytes)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: mkdir parent: %w", err)
	}
	if err := migratePlainDatabase(path, key); err != nil {
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: encrypt existing database: %w", err)
	}

	db, err := openSQLDatabase(path, key, false)
	if err != nil {
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: unlock encrypted database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: protect database permissions: %w", err)
	}

	s := &Store{
		db:       db,
		path:     path,
		keyPath:  KeyPath(path),
		key:      key,
		password: password,
	}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		zeroBytes(key)
		zeroBytes(password)
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	return s, nil
}

func openSQLDatabase(path string, key []byte, readOnly bool) (*sql.DB, error) {
	return sql.Open("sqlite3", encryptedDSN(path, key, readOnly))
}

// Close flushes and closes the underlying database. Safe to call
// multiple times.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.credentialMu.Lock()
	defer s.credentialMu.Unlock()
	var err error
	if s.db != nil {
		err = s.db.Close()
		s.db = nil
	}
	zeroBytes(s.key)
	zeroBytes(s.password)
	s.key = nil
	s.password = nil
	return err
}

// DB exposes the underlying *sql.DB for sibling packages that need to
// build their own queries. Use sparingly — most callers should add a
// typed method to this package instead.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the on-disk path the store was opened against.
func (s *Store) Path() string { return s.path }

// KeyPath returns the password-protected key-envelope file for path.
func KeyPath(path string) string { return path + ".key" }

// EncryptionKeyPath reports where this Store's password-protected key
// envelope lives. The raw AES-XTS key is never written there.
func (s *Store) EncryptionKeyPath() string {
	if s == nil {
		return ""
	}
	return s.keyPath
}

// Snapshot writes a consistent encrypted copy of the database to dest using
// the live database key. The matching password envelope must travel with it.
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	if s == nil || s.db == nil {
		return errors.New("store.Snapshot: nil store")
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("store.Snapshot: mkdir: %w", err)
	}
	// Remove a prior snapshot — VACUUM INTO refuses to overwrite.
	_ = os.Remove(dest)
	// dest is a trusted, app-controlled path; quote single quotes defensively.
	encryptedDest := encryptedVacuumURI(dest, s.key)
	q := "VACUUM INTO '" + strings.ReplaceAll(encryptedDest, "'", "''") + "'"
	if _, err := s.db.ExecContext(ctx, q); err != nil {
		return fmt.Errorf("store.Snapshot: vacuum into %s: %w", dest, err)
	}
	if err := os.Chmod(dest, 0o600); err != nil {
		return fmt.Errorf("store.Snapshot: protect snapshot: %w", err)
	}
	return nil
}

// OpenSnapshot opens either a current encrypted backup snapshot (using its
// adjacent envelope and this Store's in-memory password) or a legacy plaintext
// snapshot. The boolean is true only for the legacy plaintext format.
func (s *Store) OpenSnapshot(path, envelopePath string) (*sql.DB, bool, error) {
	if s == nil {
		return nil, false, errors.New("store.OpenSnapshot: nil store")
	}
	s.credentialMu.RLock()
	defer s.credentialMu.RUnlock()
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	header := make([]byte, len(sqliteHeader))
	n, readErr := io.ReadFull(f, header)
	_ = f.Close()
	if readErr != nil && readErr != io.ErrUnexpectedEOF {
		return nil, false, readErr
	}
	if n == len(sqliteHeader) && bytes.Equal(header, sqliteHeader) {
		db, err := sql.Open("sqlite3", plainDSN(path, true))
		return db, true, err
	}
	if len(s.password) == 0 {
		return nil, false, errors.New("backup password required; unlock the database with its password")
	}
	key, err := unlockEnvelopeFile(envelopePath, string(s.password))
	if err != nil {
		return nil, false, fmt.Errorf("unlock backup key envelope: %w", err)
	}
	defer zeroBytes(key)
	db, err := openSQLDatabase(path, key, true)
	if err != nil {
		return nil, false, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, false, fmt.Errorf("open encrypted backup snapshot: %w", err)
	}
	return db, false, nil
}

func encryptedDSN(path string, key []byte, readOnly bool) string {
	q := make(url.Values)
	q.Set("vfs", "xts")
	if readOnly {
		q.Set("mode", "ro")
	}
	q.Add("_pragma", "hexkey('"+fmt.Sprintf("%x", key)+"')")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Add("_pragma", "temp_store(memory)")
	if !readOnly {
		q.Add("_pragma", "journal_mode(WAL)")
	}
	return sqliteFileURI(path, q)
}

// sqliteFileURI builds a SQLite file URI without promoting a Windows drive
// letter to a URL authority. net/url serializes a URL Path such as C:/Users as
// file://C:/Users, which SQLite passes to Windows as the invalid UNC path
// //C:/Users. SQLite expects the drive-safe form file:C:/Users instead.
func sqliteFileURI(path string, query url.Values) string {
	pathURL := &url.URL{Path: filepath.ToSlash(path)}
	dsn := "file:" + pathURL.EscapedPath()
	if encoded := query.Encode(); encoded != "" {
		dsn += "?" + encoded
	}
	return dsn
}

// SchemaVersion returns the highest applied migration number, or 0 if
// no migrations have run.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	if err != nil {
		// If the table doesn't exist yet, treat as 0.
		if strings.Contains(err.Error(), "no such table") {
			return 0, nil
		}
		return 0, err
	}
	return v, nil
}

// migrate runs every embedded migration whose version number exceeds
// the current schema_version. Migrations are applied in lexicographic
// order; their filenames must start with a zero-padded integer.
func (s *Store) migrate(ctx context.Context) error {
	// Ensure schema_version exists.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}

	current, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		v, err := parseMigrationVersion(name)
		if err != nil {
			return fmt.Errorf("migration %q: %w", name, err)
		}
		if v <= current {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := s.applyMigration(ctx, v, name, string(body)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) applyMigration(ctx context.Context, version int, name, body string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for %s: %w", name, err)
	}
	defer tx.Rollback() //nolint:errcheck // intentional: commit replaces it
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version(version) VALUES (?)`, version); err != nil {
		return fmt.Errorf("record %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}

// parseMigrationVersion extracts the leading integer from filenames
// like "0001_init.sql".
func parseMigrationVersion(name string) (int, error) {
	base := name
	if i := strings.Index(base, "_"); i > 0 {
		base = base[:i]
	} else if i := strings.Index(base, "."); i > 0 {
		base = base[:i]
	}
	v, err := strconv.Atoi(base)
	if err != nil {
		return 0, fmt.Errorf("filename %q has no leading version", name)
	}
	return v, nil
}
