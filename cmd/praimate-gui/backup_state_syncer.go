package main

// coreStateSyncer implements backup.StateSyncer over the process-wide
// Core + launcher config. Registered once in initAppCore; from then on
// every backup commit carries the DB snapshot + shareable config, and
// every pull/merge/reset/clone row-merges the remote's snapshot back
// into the live DB. This file is intentionally mirrored from
// cmd/praimate (the two binaries share no main-package code).

import (
	"context"
	"os"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

type coreStateSyncer struct {
	core *core.Core
}

func (s coreStateSyncer) Export(ctx context.Context, repoDir string) error {
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
	if err := s.core.ImportBackupState(ctx, repoDir); err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(repoDir, core.BackupStateDir, "config.json"))
	if err != nil {
		return nil // remote has no shareable config — fine
	}
	return launcher.ApplyShareableConfig(raw)
}
