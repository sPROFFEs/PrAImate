package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestStudioFailedLaunchDoesNotEnterRecentProjects(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	s, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "studio.sqlite"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a := NewApp()
	a.ctx = context.Background()
	a.core, err = core.New(core.Options{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.StudioOpenProject(filepath.Join(t.TempDir(), "missing"), "praimate-code", "", "", "edits"); err == nil {
		t.Fatal("invalid project folder must fail without starting an editor")
	}
	projects, err := a.StudioListRecentProjects()
	if err != nil || len(projects) != 0 {
		t.Fatalf("failed launch was recorded: %v, %v", projects, err)
	}
}
