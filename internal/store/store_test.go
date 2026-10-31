package store

import (
	"context"
	"path/filepath"
	"testing"
)

const testDatabasePassword = "correct horse battery staple"

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := initializeWithPassword(path, testDatabasePassword, kdfParams{
		time: 1, memoryKiB: 8 * 1024, threads: 1,
	})
	if err != nil {
		t.Fatalf("initialize test store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestOpen_CreatesFileAndAppliesMigrations verifies that Open() bootstraps
// a fresh database, runs every embedded migration, and records the
// resulting schema version.
func TestOpen_CreatesFileAndAppliesMigrations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.sqlite")

	s := openTestStore(t, path)
	defer s.Close()

	v, err := s.SchemaVersion(context.Background())
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if v < 1 {
		t.Fatalf("schema version after Open = %d, want >= 1", v)
	}
}

// TestOpen_Idempotent verifies that a second Open() on an already-
// migrated database is a no-op and reports the same schema version.
func TestOpen_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db.sqlite")
	s1 := openTestStore(t, path)
	v1, _ := s1.SchemaVersion(context.Background())
	if err := s1.Close(); err != nil {
		t.Fatalf("close 1: %v", err)
	}

	s2, err := OpenWithPassword(path, testDatabasePassword)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	v2, _ := s2.SchemaVersion(context.Background())
	if v1 != v2 {
		t.Fatalf("schema version drift: first=%d second=%d", v1, v2)
	}
}

// TestSchemaTables_Present spot-checks that every v1 table exists after
// migration. If a table is renamed or dropped in a future migration,
// this test must be updated to match.
func TestSchemaTables_Present(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db.sqlite"))
	defer s.Close()

	want := []string{
		"chats", "messages", "agents",
		"mcp_servers", "schedules", "watchers",
		"settings_cli", "settings_gui",
		"external_agent_runs",
		"schema_version",
	}
	for _, table := range want {
		var name string
		err := s.DB().QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).
			Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}

func TestSchemaTables_CrossChatMemoryAbsent(t *testing.T) {
	s := openTestStore(t, filepath.Join(t.TempDir(), "db.sqlite"))
	defer s.Close()

	for _, table := range []string{"memory_identity", "memory_pinned", "memory_episodes"} {
		var count int
		err := s.DB().QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name = ?`, table).
			Scan(&count)
		if err != nil {
			t.Fatalf("inspect table %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("removed cross-chat memory table %s still exists", table)
		}
	}
}

// TestParseMigrationVersion covers the filename-to-int parser used by
// the migration runner.
func TestParseMigrationVersion(t *testing.T) {
	cases := []struct {
		name string
		want int
		ok   bool
	}{
		{"0001_init.sql", 1, true},
		{"0042_add_index.sql", 42, true},
		{"5.sql", 5, true},
		{"init.sql", 0, false},
		{"abc.sql", 0, false},
	}
	for _, tc := range cases {
		got, err := parseMigrationVersion(tc.name)
		if tc.ok && err != nil {
			t.Errorf("%s: unexpected error %v", tc.name, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%s: expected error, got version %d", tc.name, got)
		}
		if tc.ok && got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}
