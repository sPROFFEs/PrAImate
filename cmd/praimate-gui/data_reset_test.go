package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestDeleteAllStoredDataRequiresExactConfiguredProjectsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "data")
	t.Setenv("PRAIMATE_HOME", root)
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: filepath.Join(t.TempDir(), "projects")}); err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.ctx = context.Background()
	if err := a.DeleteAllStoredData(t.TempDir(), deleteAllDataPhrase); err == nil {
		t.Fatal("expected mismatched projects folder to be rejected")
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); err != nil {
		t.Fatalf("rejected deletion changed app data: %v", err)
	}
}

func TestDeleteAllStoredDataRemovesAppAndConfirmedProjects(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data", "praimate")
	projects := filepath.Join(base, "projects", "praimate-workspaces")
	fakeHome := filepath.Join(base, "home")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("HOME", fakeHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fakeHome, ".config"))
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projects, "chat.txt"), []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := launcher.SaveConfig(&launcher.Config{WorkspacesRoot: projects}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	a := NewApp()
	a.ctx = context.Background()
	a.st = st
	a.quit = func(context.Context) {}
	if err := a.DeleteAllStoredData(projects, deleteAllDataPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("app data still exists: %v", err)
	}
	if _, err := os.Stat(projects); !os.IsNotExist(err) {
		t.Fatalf("projects root still exists: %v", err)
	}
}
