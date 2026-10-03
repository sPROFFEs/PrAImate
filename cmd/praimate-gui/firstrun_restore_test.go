package main

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestInstallClonedLegacyPlaintextDatabaseNeedsNoEnvelope(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, core.BackupStateDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(stateDir, "db.sqlite")
	plain, err := sql.Open("sqlite3", snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Exec(`CREATE TABLE legacy (value TEXT); INSERT INTO legacy VALUES ('old backup preserved')`); err != nil {
		t.Fatal(err)
	}
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}
	a := &App{dbPath: filepath.Join(t.TempDir(), "db.sqlite")}
	if err := a.installClonedDatabase(root); err != nil {
		t.Fatal(err)
	}
	restored, err := store.InitializeWithPassword(a.dbPath, "new local database password")
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var value string
	if err := restored.DB().QueryRow(`SELECT value FROM legacy`).Scan(&value); err != nil || value != "old backup preserved" {
		t.Fatalf("legacy restore lost its data: value=%q error=%v", value, err)
	}
	needsKey, err := store.BackupNeedsKeyEnvelope(a.dbPath)
	if err != nil || !needsKey {
		t.Fatalf("legacy DB was not encrypted after password setup: %v", err)
	}
}

func TestInstallClonedEncryptedDatabaseMissingEnvelopeDoesNotCreateLocalKey(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, core.BackupStateDir)
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "source database password")
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if err := source.Snapshot(context.Background(), filepath.Join(stateDir, "db.sqlite")); err != nil {
		t.Fatal(err)
	}
	a := &App{dbPath: filepath.Join(t.TempDir(), "db.sqlite")}
	if err := a.installClonedDatabase(root); !errors.Is(err, store.ErrBackupEnvelopeMissing) {
		t.Fatalf("missing backup envelope: %v", err)
	}
	if !a.databaseFilesAbsent() {
		t.Fatal("incomplete encrypted backup created unusable local data")
	}
}

func TestFirstRunRunsBeforePasswordOnlyForEmptyInstall(t *testing.T) {
	dataRoot := t.TempDir()
	t.Setenv("PRAIMATE_HOME", dataRoot)
	a := &App{dbPath: filepath.Join(dataRoot, "db.sqlite")}

	info, err := a.FirstRun()
	if err != nil {
		t.Fatal(err)
	}
	if !info.Needed || !info.BeforeUnlock {
		t.Fatalf("fresh install = needed and before unlock, got %+v", info)
	}

	if err := os.WriteFile(a.dbPath, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = a.FirstRun()
	if err != nil {
		t.Fatal(err)
	}
	if info.BeforeUnlock {
		t.Fatalf("existing database must unlock before setup, got %+v", info)
	}
}

func TestInstallClonedDatabaseAdoptsSnapshotAndEnvelope(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, core.BackupStateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "db.sqlite")
	source, err := store.InitializeWithPassword(sourcePath, "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Snapshot(context.Background(), filepath.Join(stateDir, "db.sqlite")); err != nil {
		t.Fatal(err)
	}
	envelope, err := os.ReadFile(store.KeyPath(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "db.sqlite.key"), envelope, 0o600); err != nil {
		t.Fatal(err)
	}

	dataRoot := t.TempDir()
	a := &App{dbPath: filepath.Join(dataRoot, "db.sqlite")}
	if err := a.installClonedDatabase(root); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, store.KeyPath(a.dbPath), envelope)
	restored, err := store.OpenWithPassword(a.dbPath, "correct horse battery staple")
	if err != nil {
		t.Fatalf("restored database did not accept backup password: %v", err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInstallClonedDatabaseNeverOverwritesLocalDatabase(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, core.BackupStateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "db.sqlite"), []byte("remote"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "db.sqlite.key"), []byte("remote-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	dataRoot := t.TempDir()
	a := &App{dbPath: filepath.Join(dataRoot, "db.sqlite")}
	if err := os.WriteFile(a.dbPath, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.installClonedDatabase(root); err == nil {
		t.Fatal("expected overwrite refusal")
	}
	assertFileContent(t, a.dbPath, []byte("local"))
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
