package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/backup"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestRemoteBackupRestoreWithDifferentPasswordPreservesPendingSnapshot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	setUserConfigDir(t, t.TempDir())
	backup.SetStateSyncer(nil)
	t.Cleanup(func() { backup.SetStateSyncer(nil) })
	ctx := context.Background()
	const remotePassword = "remote backup database password"
	const localPassword = "local installation database password"
	sourceStore, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), remotePassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sourceStore.Close() })
	source, err := core.New(core.Options{Store: sourceStore})
	if err != nil {
		t.Fatal(err)
	}
	remoteChat, err := source.CreateChat(ctx, core.CreateChatRequest{Title: "Remote chat", CLIAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.AddMessage(ctx, remoteChat.ID, "user", "remote transcript", nil); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if r := backup.Run(ctx, remote, "init", "--bare", "-b", "main"); r.Failed() {
		t.Fatalf("init local bare remote: %s", backup.UserError(r))
	}
	seed := t.TempDir()
	backup.SetStateSyncer(coreStateSyncer{core: source})
	if _, err := backup.Init(ctx, seed); err != nil {
		t.Fatal(err)
	}
	if err := backup.AddRemote(ctx, seed, remote); err != nil {
		t.Fatal(err)
	}
	if err := backup.Push(ctx, seed, false); err != nil {
		t.Fatal(err)
	}

	localStore, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), localPassword)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = localStore.Close() })
	target, err := core.New(core.Options{Store: localStore})
	if err != nil {
		t.Fatal(err)
	}
	localChat, err := target.CreateChat(ctx, core.CreateChatRequest{Title: "Local chat", CLIAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "restored")
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: workspace, BackupEnabled: true, BackupRemoteURL: remote}); err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, core: target, st: localStore}
	syncer := coreStateSyncer{core: target, app: app}
	backup.SetStateSyncer(syncer)
	if err := backup.Clone(ctx, remote, workspace); !errors.Is(err, store.ErrBackupPasswordRequired) {
		t.Fatalf("clone should retain downloaded data and request its password: %v", err)
	}
	status, err := app.BackupStatus()
	if err != nil || !status.RestorePending || !status.RestorePasswordRequired {
		t.Fatalf("no password prompt state: %+v error=%v", status, err)
	}
	snapshot := filepath.Join(workspace, core.BackupStateDir, "db.sqlite")
	before, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := syncer.Export(ctx, workspace); err == nil {
		t.Fatal("pending snapshot was overwritten by Export")
	}
	if _, err := app.BackupSyncNow(); err == nil {
		t.Fatal("Sync was allowed to overwrite a pending restore")
	}
	// A restarted app must discover the pending snapshot before exporting too.
	restarted := &App{ctx: ctx, core: target, st: localStore}
	if err := (coreStateSyncer{core: target, app: restarted}).Export(ctx, workspace); !errors.Is(err, store.ErrBackupPasswordRequired) {
		t.Fatalf("restart overwrote snapshot before checking the remote key: %v", err)
	}
	if _, err := app.RestoreBackupState("incorrect remote password"); !errors.Is(err, store.ErrInvalidPassword) {
		t.Fatalf("incorrect remote password accepted: %v", err)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed restore changed the remote snapshot")
	}
	status, err = app.RestoreBackupState(remotePassword)
	if err != nil {
		t.Fatal(err)
	}
	if status.RestorePending {
		t.Fatal("successful restore still blocks sync")
	}
	for _, id := range []string{localChat.ID, remoteChat.ID} {
		if _, err := target.GetChat(ctx, id); err != nil {
			t.Fatalf("restore lost chat %s: %v", id, err)
		}
	}
	messages, err := target.ListMessages(ctx, remoteChat.ID, 0)
	if err != nil || len(messages) != 1 || messages[0].Content != "remote transcript" {
		t.Fatalf("remote conversation missing: %+v err=%v", messages, err)
	}
	if err := syncer.Import(ctx, workspace); err != nil {
		t.Fatalf("session restore should not ask for the password again: %v", err)
	}
	if err := syncer.Export(ctx, workspace); err != nil {
		t.Fatalf("sync did not resume: %v", err)
	}
	if err := localStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.OpenWithPassword(localStore.Path(), localPassword)
	if err != nil {
		t.Fatalf("restore changed local database credentials: %v", err)
	}
	_ = reopened.Close()
}

func TestBackupExportPreflightDoesNotRestoreDeletedLocalChats(t *testing.T) {
	setUserConfigDir(t, t.TempDir())
	ctx := context.Background()
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "local database password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	chat, err := c.CreateChat(ctx, core.CreateChatRequest{Title: "delete after first backup", CLIAgent: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: ctx, core: c, st: st}
	syncer := coreStateSyncer{core: c, app: app}
	repo := t.TempDir()
	if err := syncer.Export(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteChat(ctx, chat.ID); err != nil {
		t.Fatal(err)
	}
	if err := syncer.Export(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetChat(ctx, chat.ID); err == nil {
		t.Fatal("export preflight resurrected a locally deleted chat")
	}
	if err := c.ImportBackupState(ctx, repo); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetChat(ctx, chat.ID); err == nil {
		t.Fatal("new backup still contains the deleted chat")
	}
}
