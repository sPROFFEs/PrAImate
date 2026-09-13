package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/backup"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
)

func TestBackupEnableRequiresExplicitSetup(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setUserConfigDir(t, t.TempDir())
	workspace := t.TempDir()
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace}); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background()}

	_, err := app.SetBackupEnabled(true)
	if err == nil || !strings.Contains(err.Error(), "choose a setup mode") {
		t.Fatalf("enable without setup error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); !os.IsNotExist(err) {
		t.Fatalf("toggle created a repository before setup: %v", err)
	}
}

func TestConfigureBackupNewCanBePausedAndResumed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setUserConfigDir(t, t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.useConfigOnly")
	t.Setenv("GIT_CONFIG_VALUE_0", "true")
	workspace := t.TempDir()
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace}); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background()}

	result, err := app.ConfigureBackup("new", "")
	if err != nil {
		t.Fatalf("ConfigureBackup: %v", err)
	}
	if result.Action != "no_remote" || !result.State.Enabled || !result.State.Initialized {
		t.Fatalf("configured state = %+v, action=%q", result.State, result.Action)
	}
	paused, err := app.SetBackupEnabled(false)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Enabled || !paused.Initialized {
		t.Fatalf("paused state lost repository status: %+v", paused)
	}
	resumed, err := app.SetBackupEnabled(true)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !resumed.Enabled || !resumed.Initialized {
		t.Fatalf("resumed state = %+v", resumed)
	}
}

func TestConfigureBackupExistingRequiresRemoteBeforeMutation(t *testing.T) {
	setUserConfigDir(t, t.TempDir())
	workspace := t.TempDir()
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace}); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background()}

	_, err := app.ConfigureBackup("existing", "")
	if err == nil || !strings.Contains(err.Error(), "requires a remote URL") {
		t.Fatalf("missing remote error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".git")); !os.IsNotExist(err) {
		t.Fatalf("invalid setup mutated workspace: %v", err)
	}
}

func TestConfigureBackupExistingComparesWithoutOverwriting(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setUserConfigDir(t, t.TempDir())
	ctx := context.Background()

	remote := t.TempDir()
	if r := backup.Run(ctx, remote, "init", "--bare", "-b", "main"); r.Failed() {
		t.Skipf("git init --bare -b main unsupported: %s", backup.UserError(r))
	}
	seed := t.TempDir()
	if err := os.MkdirAll(filepath.Join(seed, "chats", "remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed, "chats", "remote", "MEMORY.md"), []byte("remote\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Init(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := backup.AddRemote(ctx, seed, remote); err != nil {
		t.Fatal(err)
	}
	if action, _, err := backup.Sync(ctx, seed); err != nil || action != backup.SyncActionPushed {
		t.Fatalf("seed remote: action=%q err=%v", action, err)
	}

	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "chats", "local"), 0o755); err != nil {
		t.Fatal(err)
	}
	localFile := filepath.Join(workspace, "chats", "local", "MEMORY.md")
	if err := os.WriteFile(localFile, []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace}); err != nil {
		t.Fatal(err)
	}

	app := &App{ctx: ctx}
	result, err := app.ConfigureBackup("existing", remote)
	if err != nil {
		t.Fatalf("ConfigureBackup existing: %v", err)
	}
	if result.Action != "diverged" {
		t.Fatalf("action = %q, want diverged so the user chooses", result.Action)
	}
	if body, err := os.ReadFile(localFile); err != nil || string(body) != "local\n" {
		t.Fatalf("existing setup overwrote local data: body=%q err=%v", body, err)
	}
}

func TestConfigureBackupExistingCreatesMissingWorkspaceRoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setUserConfigDir(t, t.TempDir())
	backup.SetStateSyncer(nil)
	t.Cleanup(func() { backup.SetStateSyncer(nil) })
	ctx := context.Background()

	remote := t.TempDir()
	if r := backup.Run(ctx, remote, "init", "--bare", "-b", "main"); r.Failed() {
		t.Skipf("git init --bare -b main unsupported: %s", backup.UserError(r))
	}
	workspace := filepath.Join(t.TempDir(), "missing", "workspaces")
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace}); err != nil {
		t.Fatal(err)
	}

	app := &App{ctx: ctx}
	result, err := app.ConfigureBackup("existing", remote)
	if err != nil {
		t.Fatalf("ConfigureBackup existing with missing workspace: %v", err)
	}
	if !result.State.Enabled || !result.State.Initialized || !backup.IsGitRepo(workspace) {
		t.Fatalf("configured state = %+v, git repo = %v", result.State, backup.IsGitRepo(workspace))
	}
}
