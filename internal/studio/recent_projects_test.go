package studio

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRecentProjectsRejectCorruptionAndKeepOriginal(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	dir, err := StudioDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "recent_projects.json")
	original := []byte(`{"unfinished":`)
	if err := os.WriteFile(file, original, 0600); err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(nil)
	if _, err := mgr.ListRecentProjects(); err == nil {
		t.Fatal("corrupt history must not be presented as an empty list")
	}
	if err := mgr.SaveRecentProject(t.TempDir()); err == nil {
		t.Fatal("saving must not silently replace corrupt history")
	}
	got, err := os.ReadFile(file)
	if err != nil || string(got) != string(original) {
		t.Fatalf("original history changed: %q, %v", got, err)
	}
}

func TestRecentProjectsKeepDirectoriesOnly(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	mgr := NewManager(nil)
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("not a workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SaveRecentProject(file); err != nil {
		t.Fatal(err)
	}
	if err := mgr.SaveRecentProject(dir); err != nil {
		t.Fatal(err)
	}
	projects, err := mgr.ListRecentProjects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 || projects[0] != dir {
		t.Fatalf("invalid workspace listed: %v", projects)
	}
}

func TestRecentProjectsConcurrentManagersPreserveUpdates(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	root := t.TempDir()
	const count = 16
	var wg sync.WaitGroup
	for i := range count {
		dir := filepath.Join(root, fmt.Sprintf("project-%d", i))
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := NewManager(nil).SaveRecentProject(dir); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	projects, err := NewManager(nil).ListRecentProjects()
	if err != nil || len(projects) != count {
		t.Fatalf("lost updates: %d/%d projects, %v", len(projects), count, err)
	}
}
