package main

// Backup bindings for git sync of the workspaces root.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/backup"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

// BackupState is everything the Backup settings section renders:
// config toggles + live repo status.
type BackupState struct {
	Supported  bool   `json:"supported"` // false when no workspaces root configured
	Enabled    bool   `json:"enabled"`
	RemoteURL  string `json:"remoteUrl"`
	AutoSync   bool   `json:"autoSync"`
	ForceLocal bool   `json:"forceLocal"`
	LastSyncAt string `json:"lastSyncAt"` // RFC3339, "" = never
	MachineID  string `json:"machineId"`

	Initialized    bool   `json:"initialized"`
	Clean          bool   `json:"clean"`
	Ahead          int    `json:"ahead"`
	Behind         int    `json:"behind"`
	Branch         string `json:"branch"`
	LastCommit     string `json:"lastCommit"`
	LastCommitTime string `json:"lastCommitTime"`
}

// BackupSyncResult reports one Sync run. When Action is "diverged" the
// commit lists are populated so the frontend can render the
// resolution panel.
type BackupSyncResult struct {
	Action        string       `json:"action"` // in_sync | pushed | pulled | diverged | no_remote
	LocalCommits  []string     `json:"localCommits"`
	RemoteCommits []string     `json:"remoteCommits"`
	State         *BackupState `json:"state"`
}

// backupConfig loads the shared launcher config and resolves the
// workspaces root. A missing first-run config is reported as
// unsupported, not an error.
func (a *App) backupConfig() (*launcher.Config, string, error) {
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return nil, "", err
	}
	if cfg == nil || cfg.WorkspacesRoot == "" {
		return nil, "", nil
	}
	return cfg, cfg.WorkspacesRoot, nil
}

func backupStateFrom(cfg *launcher.Config, st backup.Status) *BackupState {
	out := &BackupState{
		Supported:  true,
		Enabled:    cfg.BackupEnabled,
		RemoteURL:  cfg.BackupRemoteURL,
		AutoSync:   cfg.BackupAutoSync,
		ForceLocal: cfg.BackupForceAlwaysLocal,
		MachineID:  cfg.BackupMachineID,

		Initialized: st.Initialized,
		Clean:       st.Clean,
		Ahead:       st.Ahead,
		Behind:      st.Behind,
		Branch:      st.DefaultBranch,
		LastCommit:  st.LastCommit,
	}
	if !cfg.BackupLastSyncAt.IsZero() {
		out.LastSyncAt = cfg.BackupLastSyncAt.Format(time.RFC3339)
	}
	if !st.LastCommitTime.IsZero() {
		out.LastCommitTime = st.LastCommitTime.Format(time.RFC3339)
	}
	return out
}

// backupState re-reads config + live status. The shared helper every
// mutation returns through, so the frontend always paints fresh state.
func (a *App) backupState(ctx context.Context) (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &BackupState{Supported: false}, nil
	}
	st := backup.Status{}
	if backup.IsGitRepo(dir) {
		st, _ = backup.CurrentStatus(ctx, dir) // best-effort (offline OK)
	}
	return backupStateFrom(cfg, st), nil
}

// BackupStatus returns the current backup config + live repo status.
func (a *App) BackupStatus() (*BackupState, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	return a.backupState(ctx)
}

// SetBackupEnabled pauses or resumes an already configured backup. Initial
// setup is deliberately handled by ConfigureBackup so flipping a toggle can
// never create a repository or commit files without an explicit mode choice.
func (a *App) SetBackupEnabled(on bool) (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return &BackupState{Supported: false}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()

	if on {
		if !backup.IsGitRepo(dir) {
			return nil, fmt.Errorf("backup is not configured — choose a setup mode first")
		}
		cfg.BackupEnabled = true
		if cfg.BackupMachineID == "" {
			cfg.BackupMachineID = guiNewMachineID()
		}
		if cfg.BackupRemoteURL != "" {
			if err := backup.AddRemote(ctx, dir, cfg.BackupRemoteURL); err != nil {
				return nil, fmt.Errorf("set remote: %w", err)
			}
		}
	} else {
		_ = backup.RemoveRemote(ctx, dir)
		cfg.BackupEnabled = false
		cfg.BackupAutoSync = false
		cfg.BackupForceAlwaysLocal = false
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	return a.backupState(ctx)
}

// ConfigureBackup performs the explicit first-time setup selected in Settings.
// mode is "new" for a new local history or "existing" to attach and compare an
// existing remote. Existing remote data is never allowed to overwrite local
// files automatically; Sync surfaces divergence for a user decision.
func (a *App) ConfigureBackup(mode, remoteURL string) (*BackupSyncResult, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable: no workspaces root configured")
	}
	mode = strings.TrimSpace(mode)
	remoteURL = strings.TrimSpace(remoteURL)
	switch mode {
	case "new":
	case "existing":
		if remoteURL == "" {
			return nil, fmt.Errorf("existing backup requires a remote URL")
		}
	default:
		return nil, fmt.Errorf("unknown backup setup mode %q", mode)
	}

	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()
	if mode == "existing" {
		if _, err := backup.LsRemote(ctx, remoteURL); err != nil {
			return nil, fmt.Errorf("test existing remote: %w", err)
		}
	}
	if _, err := backup.Init(ctx, dir); err != nil {
		return nil, fmt.Errorf("git init: %w", err)
	}
	if remoteURL != "" {
		if err := backup.AddRemote(ctx, dir, remoteURL); err != nil {
			return nil, fmt.Errorf("set remote: %w", err)
		}
	}
	cfg.BackupEnabled = true
	cfg.BackupRemoteURL = remoteURL
	if cfg.BackupMachineID == "" {
		cfg.BackupMachineID = guiNewMachineID()
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	if remoteURL != "" {
		return a.BackupSyncNow()
	}
	state, err := a.backupState(ctx)
	if err != nil {
		return nil, err
	}
	return &BackupSyncResult{Action: string(backup.SyncActionNoRemote), State: state}, nil
}

// SetBackupRemote saves the remote URL and (when the repo exists)
// points origin at it immediately.
func (a *App) SetBackupRemote(url string) (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return &BackupState{Supported: false}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()

	url = strings.TrimSpace(url)
	cfg.BackupRemoteURL = url
	if url != "" && backup.IsGitRepo(dir) {
		if err := backup.AddRemote(ctx, dir, url); err != nil {
			return nil, fmt.Errorf("set remote: %w", err)
		}
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	return a.backupState(ctx)
}

// TestBackupRemote probes url with git ls-remote and returns the
// remote's default branch on success.
func (a *App) TestBackupRemote(url string) (string, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
	defer cancel()
	branch, err := backup.LsRemote(ctx, strings.TrimSpace(url))
	if err != nil {
		return "", err
	}
	return branch, nil
}

// BackupSyncNow commits local changes and auto-decides push / pull /
// nothing. On divergence it returns the two commit lists so the
// frontend can offer merge / rebase / force-push / reset.
func (a *App) BackupSyncNow() (*BackupSyncResult, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable: no workspaces root configured")
	}
	if !cfg.BackupEnabled {
		return nil, fmt.Errorf("backup is disabled — flip the master switch first")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()

	if !backup.IsGitRepo(dir) {
		if _, err := backup.Init(ctx, dir); err != nil {
			return nil, fmt.Errorf("git init: %w", err)
		}
	}
	if cfg.BackupRemoteURL != "" {
		_ = backup.AddRemote(ctx, dir, cfg.BackupRemoteURL)
	}
	restore := setMachineIDEnv(cfg.BackupMachineID)
	defer restore()

	action, st, err := backup.Sync(ctx, dir)
	if err != nil {
		return nil, err
	}
	if action == backup.SyncActionPushed || action == backup.SyncActionPulled {
		cfg.BackupLastSyncAt = time.Now().UTC()
		_ = launcher.SaveConfig(cfg)
	}
	res := &BackupSyncResult{Action: string(action), State: backupStateFrom(cfg, st)}
	if action == backup.SyncActionNeedsResolution {
		branch := st.DefaultBranch
		res.LocalCommits = backupLogLines(ctx, dir, "origin/"+branch+"..HEAD")
		res.RemoteCommits = backupLogLines(ctx, dir, "HEAD..origin/"+branch)
	}
	return res, nil
}

// ResolveBackupDivergence applies one of four reconciliations:
// "merge", "rebase", "forcepush", "reset". Conflicts come
// back as errors naming the strategy; the merge/rebase attempt is
// aborted so the repo isn't left mid-operation.
func (a *App) ResolveBackupDivergence(strategy string) (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()
	restore := setMachineIDEnv(cfg.BackupMachineID)
	defer restore()

	switch strategy {
	case "merge":
		if err := backup.MergeFromRemote(ctx, dir); err != nil {
			backup.AbortMerge(ctx, dir)
			return nil, fmt.Errorf("merge: %w", err)
		}
		if err := backup.Push(ctx, dir, false); err != nil {
			return nil, fmt.Errorf("push after merge: %w", err)
		}
	case "rebase":
		if err := backup.RebaseOntoRemote(ctx, dir); err != nil {
			backup.AbortMerge(ctx, dir)
			return nil, fmt.Errorf("rebase: %w", err)
		}
		if err := backup.Push(ctx, dir, false); err != nil {
			return nil, fmt.Errorf("push after rebase: %w", err)
		}
	case "forcepush":
		if err := backup.Push(ctx, dir, true); err != nil {
			return nil, fmt.Errorf("force push: %w", err)
		}
	case "reset":
		if err := backup.ResetHardToRemote(ctx, dir); err != nil {
			return nil, fmt.Errorf("reset from remote: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown strategy %q (merge|rebase|forcepush|reset)", strategy)
	}
	cfg.BackupLastSyncAt = time.Now().UTC()
	_ = launcher.SaveConfig(cfg)
	return a.backupState(ctx)
}

// BackupForcePush commits local changes and force-pushes over the
// remote. The frontend confirms before calling.
func (a *App) BackupForcePush() (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()
	restore := setMachineIDEnv(cfg.BackupMachineID)
	defer restore()
	if err := backup.CommitLocalChanges(ctx, dir, "force push from GUI"); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	if err := backup.Push(ctx, dir, true); err != nil {
		return nil, fmt.Errorf("force push: %w", err)
	}
	cfg.BackupLastSyncAt = time.Now().UTC()
	_ = launcher.SaveConfig(cfg)
	return a.backupState(ctx)
}

// BackupResetFromRemote discards local state in favor of the remote.
// DESTRUCTIVE — the frontend double-confirms before calling.
func (a *App) BackupResetFromRemote() (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 120*time.Second)
	defer cancel()
	if err := backup.ResetHardToRemote(ctx, dir); err != nil {
		return nil, err
	}
	return a.backupState(ctx)
}

// BackupDisconnect clears the remote and turns auto-sync off; local
// files and history stay.
func (a *App) BackupDisconnect() (*BackupState, error) {
	cfg, dir, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	_ = backup.RemoveRemote(ctx, dir)
	cfg.BackupRemoteURL = ""
	cfg.BackupAutoSync = false
	cfg.BackupForceAlwaysLocal = false
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	return a.backupState(ctx)
}

// SetBackupAutoSync toggles the GUI's sync-on-startup/exit hooks.
func (a *App) SetBackupAutoSync(on bool) (*BackupState, error) {
	cfg, _, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	cfg.BackupAutoSync = on
	if on && cfg.BackupMachineID == "" {
		cfg.BackupMachineID = guiNewMachineID()
	}
	if !on {
		cfg.BackupForceAlwaysLocal = false
	}
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	return a.backupState(ctx)
}

// SetBackupForceLocal toggles the force-always-local divergence policy
// (guarded by the machine-id + 24h window in the auto-sync hooks).
func (a *App) SetBackupForceLocal(on bool) (*BackupState, error) {
	cfg, _, err := a.backupConfig()
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("backup unavailable")
	}
	if on && !cfg.BackupAutoSync {
		return nil, fmt.Errorf("force-local is a sub-option of auto-sync — enable auto-sync first")
	}
	cfg.BackupForceAlwaysLocal = on
	if err := launcher.SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}
	ctx, cancel := context.WithTimeout(a.ctx, 60*time.Second)
	defer cancel()
	return a.backupState(ctx)
}

// --- helpers ---------------------------------------------------------------

// guiNewMachineID creates a short stable identifier for force-push guards.
func guiNewMachineID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// setMachineIDEnv exports the machine id for the duration of an op so
// backup.CommitLocalChanges stamps the Machine-ID trailer, matching
// backup operations. Returns the restore func.
func setMachineIDEnv(id string) func() {
	if id == "" {
		return func() {}
	}
	_ = os.Setenv("PRAIMATE_BACKUP_MACHINE_ID", id)
	return func() { _ = os.Unsetenv("PRAIMATE_BACKUP_MACHINE_ID") }
}

// backupLogLines lists commits in range for the divergence panel.
func backupLogLines(ctx context.Context, dir, rangeSpec string) []string {
	res := backup.Run(ctx, dir, "log", "--format=%h %ai %s", rangeSpec)
	if res.Failed() {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(res.Stdout), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
