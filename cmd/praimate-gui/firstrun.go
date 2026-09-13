package main

// First-run setup:
// when no launcher config exists, the frontend shows a setup screen
// asking for the workspaces root, whether to seed the bundled sample
// templates, and (optionally) a git remote to clone an existing backup
// from. Completing it writes config.json and rebuilds Core on the new
// root, so the app continues without a restart.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/backup"
	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

// FirstRunInfo tells the frontend whether to show setup and what to
// pre-fill.
type FirstRunInfo struct {
	Needed       bool   `json:"needed"`
	BeforeUnlock bool   `json:"beforeUnlock"`
	DefaultRoot  string `json:"defaultRoot"`
}

// FirstRun reports whether the launcher config exists yet.
func (a *App) FirstRun() (*FirstRunInfo, error) {
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	needed := cfg == nil || cfg.WorkspacesRoot == ""
	return &FirstRunInfo{
		Needed:       needed,
		BeforeUnlock: needed && a.databaseFilesAbsent(),
		DefaultRoot:  filepath.Join(home, "praimate-workspaces"),
	}, nil
}

// databaseFilesAbsent reports whether this is a genuinely fresh install. A
// machine with an existing DB but a missing launcher config must unlock first;
// otherwise the restore wizard could replace data that belongs to that DB.
func (a *App) databaseFilesAbsent() bool {
	if a.dbPath == "" {
		return false
	}
	for _, path := range []string{a.dbPath, store.KeyPath(a.dbPath)} {
		if _, err := os.Stat(path); err == nil || !errors.Is(err, os.ErrNotExist) {
			return false
		}
	}
	return true
}

// CompleteFirstRun creates the workspaces root (or clones it from a
// backup remote), optionally seeds the bundled sample templates, saves
// the config and rebinds Core to the new root. The options are empty
// root, root plus samples, or clone from remote.
func (a *App) CompleteFirstRun(root string, seedSamples bool, seedAgents bool, cloneURL string) error {
	root = strings.TrimSpace(root)
	if root == "" {
		return errors.New("a workspaces folder is required")
	}
	cloneURL = strings.TrimSpace(cloneURL)

	cfg, err := launcher.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &launcher.Config{}
	}
	cfg.WorkspacesRoot = root
	if cloneURL != "" {
		// Probe before cloning so a typo'd URL fails with a clear
		// message instead of a half-created root.
		ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
		defer cancel()
		if _, err := backup.LsRemote(ctx, cloneURL); err != nil {
			return fmt.Errorf("can't reach the remote: %w", err)
		}
		if entries, err := os.ReadDir(root); err == nil && len(entries) > 0 {
			return fmt.Errorf("%s exists and is not empty — git clone needs a new or empty folder", root)
		}
		cctx, ccancel := context.WithTimeout(a.ctx, 10*time.Minute)
		defer ccancel()
		if err := backup.Clone(cctx, cloneURL, root); err != nil {
			return err
		}
		// A new machine must adopt the encrypted DB snapshot before it creates
		// a local password/key. The next screen then asks for the backup's
		// existing password and opens the restored DB directly.
		if err := a.installClonedDatabase(root); err != nil {
			return err
		}
		cfg.BackupEnabled = true
		cfg.BackupRemoteURL = cloneURL
	} else {
		for _, sub := range []string{"chats", "templates"} {
			if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
				return fmt.Errorf("create %s: %w", root, err)
			}
		}
		if seedSamples {
			exe, err := os.Executable()
			if err == nil {
				execDir := filepath.Dir(exe)
				// Best-effort: a bundle without samples just seeds nothing.
				_, _ = launcher.SeedSamples(root, launcher.SampleCandidates(execDir))
			}
		}
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		return err
	}

	// Rebind Core to the new root so workspace chats and the backup tab
	// work immediately, no restart needed.
	if a.st != nil {
		if c, err := core.New(core.Options{Store: a.st, WorkspacesRoot: root}); err == nil {
			a.core = c
			c.SetApprovalProvider(a.approvalProvider)
			backup.SetStateSyncer(coreStateSyncer{core: c})
		}
	}

	// Opt-in: import the curated sample agents (reverse-ghidra,
	// code-review, dev-team, security-review, agent-builder). Best-effort
	// — a bundle without samples/agents/ just imports nothing. Skipped
	// when cloning a backup (that already carries the user's agents).
	if seedAgents && cloneURL == "" && a.core != nil {
		exe, err := os.Executable()
		if err == nil {
			dir := launcher.FirstExistingDir(launcher.SampleAgentCandidates(filepath.Dir(exe)))
			if dir != "" {
				ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
				_, _ = a.core.SeedSampleAgents(ctx, dir)
				cancel()
			}
		}
	}
	return nil
}

func (a *App) installClonedDatabase(root string) error {
	snapshot := filepath.Join(root, core.BackupStateDir, "db.sqlite")
	envelope := snapshot + ".key"
	snapshotInfo, snapshotErr := os.Stat(snapshot)
	_, envelopeErr := os.Stat(envelope)
	if errors.Is(snapshotErr, os.ErrNotExist) && errors.Is(envelopeErr, os.ErrNotExist) {
		return nil // legacy backup: chats/templates only; create a new DB next
	}
	if snapshotErr != nil {
		return fmt.Errorf("restore backup database: %w", snapshotErr)
	}
	if envelopeErr != nil {
		return fmt.Errorf("restore backup password envelope: %w", envelopeErr)
	}
	if snapshotInfo.IsDir() {
		return errors.New("restore backup database: snapshot is a directory")
	}
	if a.dbPath == "" {
		return errors.New("restore backup database: local database path is unavailable")
	}
	if !a.databaseFilesAbsent() {
		return errors.New("restore backup database: local database already exists; unlock it before connecting a backup")
	}
	if err := os.MkdirAll(filepath.Dir(a.dbPath), 0o700); err != nil {
		return fmt.Errorf("restore backup database: create data folder: %w", err)
	}
	if err := copyNewPrivateFile(snapshot, a.dbPath); err != nil {
		return fmt.Errorf("restore backup database: %w", err)
	}
	if err := copyNewPrivateFile(envelope, store.KeyPath(a.dbPath)); err != nil {
		_ = os.Remove(a.dbPath) // rollback the DB created by this function
		return fmt.Errorf("restore backup password envelope: %w", err)
	}
	return nil
}

func copyNewPrivateFile(source, destination string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			_ = os.Remove(destination)
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return nil
}
