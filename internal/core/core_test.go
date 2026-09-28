package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestNew_RequiresAtLeastOneInput(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error when both Store and WorkspacesRoot are zero")
	}
}

func TestNew_AcceptsStoreOnly(t *testing.T) {
	s := openTempStore(t)
	defer s.Close()
	c, err := New(Options{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Store() != s {
		t.Fatal("Store() should return the supplied store")
	}
	if c.WorkspacesRoot() != "" {
		t.Fatalf("WorkspacesRoot should be empty, got %q", c.WorkspacesRoot())
	}
}

func TestListAgents_EmptyOnFreshDB(t *testing.T) {
	s := openTempStore(t)
	defer s.Close()
	c, err := New(Options{Store: s})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	agents, err := c.ListAgents(context.Background())
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 0 {
		t.Fatalf("expected 0 agents on fresh DB, got %d", len(agents))
	}
}

func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return s
}
