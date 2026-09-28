package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestExportBackupState_SnapshotsDBAndAgents(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	ctx := context.Background()
	if _, err := c.SeedBuiltins(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	repo := t.TempDir()
	if err := c.ExportBackupState(ctx, repo); err != nil {
		t.Fatalf("ExportBackupState: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, BackupStateDir, "db.sqlite")); err != nil {
		t.Fatalf("db snapshot missing: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(repo, BackupStateDir, "agents"))
	if len(entries) < 3 {
		t.Fatalf("expected ≥3 agent yaml exports, got %d", len(entries))
	}
}

func TestExportBackupState_EncryptsAndPreservesStructuredSecrets(t *testing.T) {
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	chat, err := c.CreateChat(ctx, CreateChatRequest{
		Title: "secret", CLIAgent: "claude",
		Settings: ChatSettings{Local: &ChatLocalEndpoint{
			Endpoint: "https://llm.example", APIKey: "chat-secret", Model: "m",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ConnectMCP(ctx, ConnectMCPRequest{
		ID: "private", Name: "private", Transport: MCPTransportHTTP,
		URL: "https://mcp.example", Env: map[string]string{"TOKEN": "mcp-secret"},
		Auth: map[string]string{"type": "bearer", "token": "auth-secret"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(ctx, ScopeCLI, "local_llm.api_key", []byte(`"default-secret"`)); err != nil {
		t.Fatal(err)
	}

	repo := t.TempDir()
	if err := c.ExportBackupState(ctx, repo); err != nil {
		t.Fatal(err)
	}
	snapPath := filepath.Join(repo, BackupStateDir, "db.sqlite")
	raw, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) >= 16 && string(raw[:16]) == "SQLite format 3\x00" {
		t.Fatal("backup DB is plaintext")
	}
	if _, err := os.Stat(snapPath + ".key"); err != nil {
		t.Fatalf("backup key envelope missing: %v", err)
	}
	snap, legacy, err := c.store.OpenSnapshot(snapPath, snapPath+".key")
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	if legacy {
		t.Fatal("current backup unexpectedly opened as legacy plaintext")
	}
	var settings, envJSON, authJSON string
	if err := snap.QueryRow(`SELECT settings_json FROM chats WHERE id = ?`, chat.ID).Scan(&settings); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(settings, "chat-secret") {
		t.Fatalf("encrypted backup lost chat API key: %s", settings)
	}
	if err := snap.QueryRow(`SELECT env_json, auth_json FROM mcp_servers WHERE id = 'private'`).Scan(&envJSON, &authJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(envJSON, "mcp-secret") || !strings.Contains(authJSON, "auth-secret") {
		t.Fatalf("encrypted backup lost MCP secrets: env=%s auth=%s", envJSON, authJSON)
	}
	var count int
	if err := snap.QueryRow(`SELECT COUNT(*) FROM settings_cli WHERE key = 'local_llm.api_key'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("encrypted backup lost saved local LLM key")
	}
}

func TestSanitizeImportedRowRejectsLegacyBackupCredentials(t *testing.T) {
	settingsCols := []string{"key", "value_json"}
	settingsVals := []any{"local_llm.api_key", `"remote-secret"`}
	if sanitizeImportedRow("settings_cli", settingsCols, settingsVals) {
		t.Fatal("legacy local LLM credential row should not import")
	}

	mcpCols := []string{"id", "env_json", "auth_json"}
	mcpVals := []any{"mcp", `{"TOKEN":"secret"}`, `{"token":"secret"}`}
	if !sanitizeImportedRow("mcp_servers", mcpCols, mcpVals) {
		t.Fatal("MCP metadata row should still import")
	}
	if mcpVals[1] != "{}" || mcpVals[2] != "{}" {
		t.Fatalf("MCP credentials not stripped: %#v", mcpVals)
	}

	chatCols := []string{"id", "settings_json"}
	chatVals := []any{"chat", `{"local":{"endpoint":"https://llm","api_key":"secret","model":"m"}}`}
	if !sanitizeImportedRow("chats", chatCols, chatVals) {
		t.Fatal("chat row should still import")
	}
	if strings.Contains(stringValue(chatVals[1]), "secret") {
		t.Fatalf("chat credential not stripped: %s", chatVals[1])
	}
}

func TestPortableSQLiteURIPreservesWindowsDriveAndEscapesPath(t *testing.T) {
	got := portableSQLiteURI(`C:/Users/test user/backup#1.sqlite`)
	const want = `file:C:/Users/test%20user/backup%231.sqlite`
	if got != want {
		t.Fatalf("URI = %q, want %q", got, want)
	}
}

// Two machines, one remote: machine A's chats + messages must appear on
// machine B after importing A's snapshot, without disturbing B's own
// rows. This is the multi-host sharing contract.
func TestImportBackupState_MergesChatsAcrossMachines(t *testing.T) {
	ctx := context.Background()
	a, _ := New(Options{Store: openTempStore(t)})
	b, _ := New(Options{Store: openTempStore(t)})

	chA, err := a.CreateChat(ctx, CreateChatRequest{Title: "from machine A", CLIAgent: "claude"})
	if err != nil {
		t.Fatalf("create chat A: %v", err)
	}
	if _, err := a.AddMessage(ctx, chA.ID, "user", "hello from A", nil); err != nil {
		t.Fatalf("add message A: %v", err)
	}
	chB, _ := b.CreateChat(ctx, CreateChatRequest{Title: "machine B local", CLIAgent: "codex"})

	repo := t.TempDir()
	if err := a.ExportBackupState(ctx, repo); err != nil {
		t.Fatalf("export A: %v", err)
	}
	if err := b.ImportBackupState(ctx, repo); err != nil {
		t.Fatalf("import into B: %v", err)
	}

	got, err := b.GetChat(ctx, chA.ID)
	if err != nil {
		t.Fatalf("A's chat missing on B after import: %v", err)
	}
	if got.Title != "from machine A" {
		t.Fatalf("title = %q", got.Title)
	}
	msgs, _ := b.ListMessages(ctx, chA.ID, 0)
	if len(msgs) != 1 || msgs[0].Content != "hello from A" {
		t.Fatalf("A's messages not merged: %+v", msgs)
	}
	if _, err := b.GetChat(ctx, chB.ID); err != nil {
		t.Fatalf("B's own chat lost after import: %v", err)
	}

	// Idempotence: importing the same snapshot again must not duplicate
	// messages (natural-key dedupe, not autoincrement ids).
	if err := b.ImportBackupState(ctx, repo); err != nil {
		t.Fatalf("re-import: %v", err)
	}
	msgs, _ = b.ListMessages(ctx, chA.ID, 0)
	if len(msgs) != 1 {
		t.Fatalf("re-import duplicated messages: got %d", len(msgs))
	}
}

func TestImportBackupState_RejectsDifferentDatabasePassword(t *testing.T) {
	ctx := context.Background()
	source, _ := New(Options{Store: openTempStore(t)})
	otherStore, err := store.InitializeWithPassword(
		filepath.Join(t.TempDir(), "db.sqlite"),
		"a completely different database password",
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherStore.Close() })
	target, _ := New(Options{Store: otherStore})

	repo := t.TempDir()
	if err := source.ExportBackupState(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := target.ImportBackupState(ctx, repo); err == nil {
		t.Fatal("encrypted backup opened with a different database password")
	}
}

// Keyed rows follow newer-updated_at-wins: a title change on machine A
// must overwrite B's stale copy, but a stale snapshot must never
// clobber a newer local row.
func TestImportBackupState_NewerUpdatedAtWins(t *testing.T) {
	ctx := context.Background()
	a, _ := New(Options{Store: openTempStore(t)})
	b, _ := New(Options{Store: openTempStore(t)})

	ch, _ := a.CreateChat(ctx, CreateChatRequest{Title: "v1", CLIAgent: "claude"})
	repo := t.TempDir()
	if err := a.ExportBackupState(ctx, repo); err != nil {
		t.Fatalf("export v1: %v", err)
	}
	if err := b.ImportBackupState(ctx, repo); err != nil {
		t.Fatalf("import v1: %v", err)
	}

	// Bump the row on A with a strictly newer updated_at.
	if _, err := a.store.DB().ExecContext(ctx,
		`UPDATE chats SET title = 'v2', updated_at = '2999-01-01T00:00:00Z' WHERE id = ?`, ch.ID); err != nil {
		t.Fatalf("bump A: %v", err)
	}
	if err := a.ExportBackupState(ctx, repo); err != nil {
		t.Fatalf("export v2: %v", err)
	}
	if err := b.ImportBackupState(ctx, repo); err != nil {
		t.Fatalf("import v2: %v", err)
	}
	got, _ := b.GetChat(ctx, ch.ID)
	if got.Title != "v2" {
		t.Fatalf("newer remote row should win; title = %q", got.Title)
	}

	// Now B is newer than the (stale) snapshot — re-import must keep v3.
	if _, err := b.store.DB().ExecContext(ctx,
		`UPDATE chats SET title = 'v3', updated_at = '3000-01-01T00:00:00Z' WHERE id = ?`, ch.ID); err != nil {
		t.Fatalf("bump B: %v", err)
	}
	if err := b.ImportBackupState(ctx, repo); err != nil {
		t.Fatalf("re-import stale: %v", err)
	}
	got, _ = b.GetChat(ctx, ch.ID)
	if got.Title != "v3" {
		t.Fatalf("stale snapshot clobbered newer local row; title = %q", got.Title)
	}
}
