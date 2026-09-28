package main

import (
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func TestBackupAutoSyncEnabledRequiresCompleteConfig(t *testing.T) {
	cfg := &launcher.Config{
		WorkspacesRoot:  t.TempDir(),
		BackupEnabled:   true,
		BackupAutoSync:  true,
		BackupRemoteURL: "https://example.invalid/repo.git",
	}
	if !backupAutoSyncEnabled(cfg) {
		t.Fatal("complete auto-sync config should be enabled")
	}
	cfg.BackupRemoteURL = ""
	if backupAutoSyncEnabled(cfg) {
		t.Fatal("auto-sync without a remote must be disabled")
	}
	if backupAutoSyncEnabled(nil) {
		t.Fatal("nil config must be disabled")
	}
}
