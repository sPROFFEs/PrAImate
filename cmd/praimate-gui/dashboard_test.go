package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestUsageDashboardRequiresUnlockedCoreAndReturnsProfileMetrics(t *testing.T) {
	a := NewApp()
	if _, err := a.UsageDashboard(""); err == nil {
		t.Fatal("dashboard available while locked")
	}
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	s, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "usage.sqlite"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	a.core, err = core.New(core.Options{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	u, err := a.core.BeginUsage(context.Background(), "praimate-cli", "model", "chat")
	if err != nil {
		t.Fatal(err)
	}
	u.Observe(core.StreamEvent{Type: "usage", Usage: &core.NativeUsage{PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20}})
	if err := u.Finish(nil); err != nil {
		t.Fatal(err)
	}
	d, err := a.UsageDashboard("")
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals.Runs != 1 || d.Totals.Tokens != 20 {
		t.Fatalf("incorrect profile totals: %+v", d)
	}
}
