package orchestrator

import (
	"context"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDependencyConflictCanContinueAndMergeWithoutLosingResolution(t *testing.T) {
	ctx := context.Background()
	dir := gitFixture(t)
	w := WorktreeManager{Workspace: dir, RunID: "conflict-recovery"}
	base, branch, err := w.Repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := w.Create(ctx, "a", base)
	b, _ := w.Create(ctx, "b", base)
	c, _ := w.Create(ctx, "c", base)
	defer func() {
		for _, tree := range []*Worktree{a, b, c} {
			_ = w.Remove(ctx, tree)
		}
	}()
	_ = os.WriteFile(filepath.Join(a.Path, "a.go"), []byte("A\n"), 0600)
	_ = os.WriteFile(filepath.Join(b.Path, "a.go"), []byte("B\n"), 0600)
	ra, _ := w.Complete(ctx, a, WorkerConfig{})
	rb, _ := w.Complete(ctx, b, WorkerConfig{})
	commits := []string{ra.ResultCommit, rb.ResultCommit}
	if err := w.IntegrateDependencies(ctx, c, commits); err == nil {
		t.Fatal("expected conflict")
	}
	// Staging an unresolved file must not turn the failure into a successful pick.
	if _, err := gitCommand(ctx, c.Path, "add", "--", "a.go"); err != nil {
		t.Fatal(err)
	}
	if err := w.IntegrateDependencies(ctx, c, commits); err == nil {
		t.Fatal("staged conflict markers accepted")
	}
	_ = os.WriteFile(filepath.Join(c.Path, "a.go"), []byte("A and B\n"), 0600)
	if _, err := gitCommand(ctx, c.Path, "add", "--", "a.go"); err != nil {
		t.Fatal(err)
	}
	if err := w.IntegrateDependencies(ctx, c, commits); err != nil {
		t.Fatalf("resolved task cannot resume: %v", err)
	}
	if len(c.IntegratedCommits) != 2 {
		t.Fatal("dependency checkpoint missing")
	}
	_ = os.WriteFile(filepath.Join(c.Path, "c.go"), []byte("C\n"), 0600)
	rc, err := w.Complete(ctx, c, WorkerConfig{})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := w.Merge(ctx, branch, append(commits, rc.ResultCommit))
	if merged != nil {
		defer w.Remove(ctx, merged)
	}
	if err != nil {
		t.Fatalf("review lost the conflict resolution: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	if string(data) != "A and B\n" {
		t.Fatalf("resolution lost: %s", data)
	}
}

type conflictAdapter struct {
	dagAdapter
	mu    sync.Mutex
	calls map[string]int
}

func (a *conflictAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	if opts.Model == "planner" {
		return &core.Reply{Text: `{"tasks":[{"id":"a","description":"A change","dependencies":[],"worker":{"profile":"middle","model":"A"}},{"id":"b","description":"B change","dependencies":[],"worker":{"profile":"middle","model":"B"}},{"id":"c","description":"Dependent change","dependencies":["a","b"],"worker":{"profile":"fast","model":"C"}}]}`}, nil
	}
	key := opts.Model
	file, content := "a.go", opts.Model+"\n"
	if opts.Model == "C" {
		if strings.Contains(opts.Message, "Resolve Git dependency conflicts only") {
			key = "resolve"
			content = "A and B\n"
		} else {
			file = "c.go"
			content = "C\n"
		}
	}
	a.mu.Lock()
	a.calls[key]++
	a.mu.Unlock()
	if err := os.WriteFile(filepath.Join(opts.Cwd, file), []byte(content), 0600); err != nil {
		return nil, err
	}
	return &core.Reply{Text: `{"action":"final","content":"updated files"}`}, nil
}

func TestWorkerConflictResolutionSurvivesRestartAndUnblocksDependents(t *testing.T) {
	ctx := context.Background()
	dir := gitFixture(t)
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &conflictAdapter{dagAdapter: dagAdapter{name: "codex"}, calls: map[string]int{}}
	previous, _ := core.GetCLIAdapter("codex")
	core.RegisterCLIAdapter(adapter)
	defer func() {
		if previous != nil {
			core.RegisterCLIAdapter(previous)
		} else {
			core.UnregisterCLIAdapter("codex")
		}
	}()
	config := Config{Workspace: dir, AccessMode: "full"}
	for _, tier := range []Tier{Primary, Middle, Fast} {
		config.Profiles = append(config.Profiles, Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "planner", MaxInputBytes: 32768, TimeoutSeconds: 10})
	}
	manager := NewManager(c, ctx)
	id, err := manager.PlanDAG("Changes with a conflicting dependency", config, 2)
	if err != nil {
		t.Fatal(err)
	}
	waitGraph(t, manager, id, "draft")
	if err := manager.ExecuteDAG(id, nil); err != nil {
		t.Fatal(err)
	}
	failed := waitGraph(t, manager, id, "failed")
	if failed.DAG.Tasks[2].FailurePhase != "dependencies" || failed.DAG.Tasks[0].Status != "completed" {
		t.Fatalf("wrong failure state: %+v", failed.DAG.Tasks)
	}
	if err := manager.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	manager = NewManager(c, ctx)
	defer manager.Stop(ctx)
	if err := manager.ResolveDAGTaskConflicts(id, "c"); err != nil {
		t.Fatal(err)
	}
	if err := manager.ExecuteDAG(id, nil); err != nil {
		t.Fatal(err)
	}
	completed := waitGraph(t, manager, id, "review")
	for _, task := range completed.DAG.Tasks {
		if task.Status != "completed" {
			t.Fatalf("task still blocked: %+v", task)
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	for _, name := range []string{"A", "B", "resolve", "C"} {
		if adapter.calls[name] != 1 {
			t.Fatalf("completed work repeated or recovery absent: %v", adapter.calls)
		}
	}
}
