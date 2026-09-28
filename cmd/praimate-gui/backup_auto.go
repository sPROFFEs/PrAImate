package main

import (
	"context"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/backup"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func backupAutoSyncEnabled(cfg *launcher.Config) bool {
	return cfg != nil &&
		cfg.BackupEnabled &&
		cfg.BackupAutoSync &&
		cfg.BackupRemoteURL != "" &&
		cfg.WorkspacesRoot != ""
}

// runGUIAutoSync preserves the former launcher lifecycle behavior in
// the GUI-only application. It is best-effort: network or git failures
// must not prevent the desktop app from starting or shutting down.
func runGUIAutoSync(parent context.Context, cfg *launcher.Config) {
	if !backupAutoSyncEnabled(cfg) {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	dir := cfg.WorkspacesRoot
	if !backup.IsGitRepo(dir) {
		if _, err := backup.Init(ctx, dir); err != nil {
			return
		}
		if err := backup.AddRemote(ctx, dir, cfg.BackupRemoteURL); err != nil {
			return
		}
	}

	restore := setMachineIDEnv(cfg.BackupMachineID)
	defer restore()
	action, _, err := backup.Sync(ctx, dir)
	if err != nil {
		return
	}
	switch action {
	case backup.SyncActionPushed, backup.SyncActionPulled:
		cfg.BackupLastSyncAt = time.Now().UTC()
		_ = launcher.SaveConfig(cfg)
	case backup.SyncActionNeedsResolution:
		if !cfg.BackupForceAlwaysLocal || !guiMachineGuardOK(ctx, cfg, dir) {
			return
		}
		if backup.Push(ctx, dir, true) == nil {
			cfg.BackupLastSyncAt = time.Now().UTC()
			_ = launcher.SaveConfig(cfg)
		}
	}
}

func guiMachineGuardOK(ctx context.Context, cfg *launcher.Config, dir string) bool {
	remoteID, remoteWhen, err := backup.LastCommitMachineID(ctx, dir)
	if err != nil || remoteID == "" || remoteID == cfg.BackupMachineID {
		return true
	}
	return time.Since(remoteWhen) > 24*time.Hour
}
