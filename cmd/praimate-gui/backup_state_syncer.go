package main

// coreStateSyncer implements backup.StateSyncer over the process-wide
// Core + launcher config. Registered once in initAppCore; from then on
// every backup commit carries the DB snapshot + shareable config, and
// every pull/merge/reset/clone row-merges the remote's snapshot back
// into the live DB. This file is intentionally mirrored from
// cmd/praimate (the two binaries share no main-package code).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

type coreStateSyncer struct {
	core *core.Core
	app  *App
}

func (s coreStateSyncer) Export(ctx context.Context, repoDir string) error {
	if s.app != nil {
		s.app.backupRestoreMu.Lock()
		defer s.app.backupRestoreMu.Unlock()
		if s.app.backupRestoreDir != "" {
			return fmt.Errorf("backup restore is pending; finish it in Settings before syncing: %w", s.app.backupRestoreErr)
		}
	}
	// Also check an existing snapshot after a restart. Exporting first could
	// overwrite a downloaded remote DB that has not yet been unlocked/imported.
	if err := s.core.ValidateBackupState(ctx, repoDir); err != nil {
		s.markPending(repoDir, err)
		return err
	}
	if err := s.core.ExportBackupState(ctx, repoDir); err != nil {
		return err
	}
	raw, err := launcher.ShareableConfigJSON()
	if err != nil || raw == nil {
		return err
	}
	return os.WriteFile(filepath.Join(repoDir, core.BackupStateDir, "config.json"), raw, 0o644)
}

func (s coreStateSyncer) Import(ctx context.Context, repoDir string) error {
	if s.app != nil {
		s.app.backupRestoreMu.Lock()
		defer s.app.backupRestoreMu.Unlock()
	}
	if err := s.core.ImportBackupState(ctx, repoDir); err != nil {
		s.markPending(repoDir, err)
		return err
	}
	if err := applyBackupConfig(repoDir); err != nil {
		s.markPending(repoDir, err)
		return err
	}
	s.markPending("", nil)
	return nil
}

// Called while holding backupRestoreMu when an App owns this syncer.
func (s coreStateSyncer) markPending(repoDir string, err error) {
	if s.app != nil {
		s.app.backupRestoreDir, s.app.backupRestoreErr = repoDir, err
	}
}

func applyBackupConfig(repoDir string) error {
	raw, err := os.ReadFile(filepath.Join(repoDir, core.BackupStateDir, "config.json"))
	if err != nil {
		return nil // remote has no shareable config — fine
	}
	return launcher.ApplyShareableConfig(raw)
}
