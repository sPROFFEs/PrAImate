package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

type persistentAssignmentAdapter struct {
	dagAdapter
	mu    sync.Mutex
	calls []string
}

func (*persistentAssignmentAdapter) SupportsResume() bool { return true }
func (a *persistentAssignmentAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, "fresh:"+opts.Model)
	return &core.Reply{SessionID: "assignment-session", Text: `{"action":"final","content":"done"}`}, nil
}
func (a *persistentAssignmentAdapter) Resume(_ context.Context, id string, opts core.ResumeOpts) (*core.Reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, "resume:"+id+":"+opts.Message)
	return &core.Reply{SessionID: id, Text: `{"action":"final","content":"continued"}`}, nil
}

func TestWorkerChatRestoresSessionAndStartsFreshWhenProfileChanges(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	cfg.Profiles[0].Runtime = "cli"
	cfg.Profiles[0].CLI = "codex"
	cfg.Profiles[0].Endpoint = ""
	cfg.Profiles[0].MaxOutputTokens = 0
	adapter := &persistentAssignmentAdapter{dagAdapter: dagAdapter{name: "codex"}}
	old, _ := core.GetCLIAdapter("codex")
	core.RegisterCLIAdapter(adapter)
	defer func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter("codex")
		}
	}()
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(c, ctx)
	id, err := manager.StartWithConfigAndApproval("first task", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitGraph(t, manager, id, "completed")
	for _, task := range []string{"second task", "third task"} {
		if err := manager.Stop(ctx); err != nil {
			t.Fatal(err)
		}
		manager = NewManager(c, ctx)
		if err := manager.Continue(id, task); err != nil {
			t.Fatal(err)
		}
		run := waitGraph(t, manager, id, "completed")
		if run.Attempts[len(run.Attempts)-1].SessionID != "assignment-session" {
			t.Fatal("resumed attempt lost its session")
		}
	}
	cfg.Profiles[0].Model = "replacement-model"
	if err := manager.UpdateRunConfig(id, cfg); err != nil {
		t.Fatal(err)
	}
	if err := manager.Continue(id, "fourth task"); err != nil {
		t.Fatal(err)
	}
	waitGraph(t, manager, id, "completed")
	_ = manager.Stop(ctx)
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	want := []string{"fresh:model-primary", "resume:assignment-session:second task", "resume:assignment-session:third task", "fresh:replacement-model"}
	if strings.Join(adapter.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("session continuation: %v", adapter.calls)
	}
}

func TestDependencyContinuationKeepsWorkerCommitsInResult(t *testing.T) {
	ctx := context.Background()
	w := WorktreeManager{Workspace: gitFixture(t), RunID: "dependency-continuation"}
	base, _, err := w.Repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dependency, err := w.Create(ctx, "dependency", base)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dependency.Path, "a.go"), []byte("dependency\n"), 0600)
	dep, err := w.Complete(ctx, dependency, WorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := w.Create(ctx, "task", base)
	if err != nil {
		t.Fatal(err)
	}
	commits := []string{dep.ResultCommit}
	if err := w.IntegrateDependencies(ctx, task, commits); err != nil {
		t.Fatal(err)
	}
	dependencyBase := task.BaseRef
	_ = os.WriteFile(filepath.Join(task.Path, "b.go"), []byte("worker change\n"), 0600)
	if _, err := gitCommand(ctx, task.Path, "add", "b.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := gitCommand(ctx, task.Path, "commit", "-m", "partial worker commit"); err != nil {
		t.Fatal(err)
	}
	if err := w.IntegrateDependencies(ctx, task, commits); err != nil {
		t.Fatal(err)
	}
	if task.BaseRef != dependencyBase || len(task.IntegratedCommits) != 1 {
		t.Fatal("retry moved baseline over worker changes")
	}
	result, err := w.Complete(ctx, task, WorkerConfig{})
	if err != nil || !strings.Contains(result.Diff, "worker change") {
		t.Fatalf("worker commit omitted from review: %+v %v", result, err)
	}
}
