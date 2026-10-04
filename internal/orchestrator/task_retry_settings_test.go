package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

type taskRetryCalls struct {
	mu      sync.Mutex
	workers []WorkerConfig
}

type taskRetryAdapter struct {
	dagAdapter
	calls *taskRetryCalls
}

func (a *taskRetryAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	a.calls.mu.Lock()
	defer a.calls.mu.Unlock()
	a.calls.workers = append(a.calls.workers, WorkerConfig{CLI: a.Name(), Model: opts.Model})
	if len(a.calls.workers) == 1 {
		return nil, errors.New("fixture provider unavailable")
	}
	return &core.Reply{Text: `{"action":"final","content":"retried successfully"}`}, nil
}

func TestFailedDAGTaskRetryUsesUpdatedProfileAndKeepsExplicitOverrides(t *testing.T) {
	for _, tc := range []struct {
		name               string
		worker             WorkerConfig
		wantCLI, wantModel string
		legacy, useProfile bool
	}{
		{"inherit", WorkerConfig{ProfileID: Middle}, "codex", "replacement-model", false, false},
		{"override", WorkerConfig{ProfileID: Middle, CLI: "opencode", Model: "pinned-model"}, "opencode", "pinned-model", false, false},
		{"legacy-inherit", WorkerConfig{ProfileID: Middle}, "codex", "replacement-model", true, false},
		{"legacy-override", WorkerConfig{ProfileID: Middle, CLI: "opencode", Model: "pinned-model"}, "opencode", "pinned-model", true, false},
		{"use-profile", WorkerConfig{ProfileID: Middle, CLI: "opencode", Model: "pinned-model"}, "codex", "replacement-model", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			config := testConfig(t)
			config.Workspace = gitFixture(t)
			config.Profiles[1].Runtime, config.Profiles[1].CLI = "cli", "praimate-code"
			config.Profiles[1].Model, config.Profiles[1].Endpoint, config.Profiles[1].MaxOutputTokens = "agy/broken-model", "", 0
			st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			c, err := core.New(core.Options{Store: st})
			if err != nil {
				t.Fatal(err)
			}
			calls := &taskRetryCalls{}
			for _, name := range []string{"praimate-code", "codex", "opencode"} {
				previous, _ := core.GetCLIAdapter(name)
				core.RegisterCLIAdapter(&taskRetryAdapter{dagAdapter: dagAdapter{name: name}, calls: calls})
				t.Cleanup(func() {
					if previous != nil {
						core.RegisterCLIAdapter(previous)
					} else {
						core.UnregisterCLIAdapter(name)
					}
				})
			}
			id := "worker-retry-" + tc.name
			if _, err = c.CreateChat(ctx, core.CreateChatRequest{ID: id, Title: "Retry", WorkspacePath: config.Workspace, CLIAgent: "workers", Settings: core.ChatSettings{Surface: "workers"}}); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(config)
			if _, err = c.AddMessage(ctx, id, "system", string(raw), nil); err != nil {
				t.Fatal(err)
			}
			w := WorktreeManager{Workspace: config.Workspace, RunID: id}
			base, branch, err := w.Repository(ctx)
			if err != nil {
				t.Fatal(err)
			}
			completed := DAGTask{ID: "done", Description: "Already finished", Worker: WorkerConfig{ProfileID: Fast}, Status: "completed", Review: "accepted", Output: "retained findings", Result: &TaskResult{TaskID: "done", BaseCommit: base, ResultCommit: base, WorkerConfig: WorkerConfig{ProfileID: Fast, CLI: "claude", Model: "historical-model"}}}
			m := NewManager(c, ctx)
			defer m.Stop(ctx)
			m.runs[id] = &Run{ID: id, Title: "Retry", Status: "draft", Workspace: config.Workspace, Profiles: config.Profiles, config: config, DAG: &DAG{BaseCommit: base, TargetBranch: branch, MaxParallel: 1, Tasks: []DAGTask{completed, {ID: "retry", Description: "Inspect the fixture", Dependencies: []string{"done"}, Worker: tc.worker, Status: "pending"}}}}
			m.runs[id].cancel = func() {}
			if err = m.UseDAGTaskProfile(id, "retry"); err == nil {
				t.Fatal("active task accepted a route change")
			}
			m.runs[id].cancel = nil
			if err = m.ExecuteDAG(id, nil); err != nil {
				t.Fatal(err)
			}
			failed := waitGraph(t, m, id, "failed")
			if failed.DAG.Tasks[1].Worktree == nil {
				t.Fatal("missing retained failed worktree")
			}
			oldWorker := failed.DAG.Tasks[1].Worker
			if tc.legacy {
				// Simulate a snapshot saved by the old executor after resolving and
				// overwriting the task's original requested route.
				m.mu.Lock()
				for i := range m.runs[id].DAG.Tasks {
					m.runs[id].DAG.Tasks[i].RequestedWorker = nil
				}
				m.mu.Unlock()
			}
			updated := config
			updated.Profiles = append([]Profile(nil), config.Profiles...)
			updated.Profiles[1].CLI, updated.Profiles[1].Model = "codex", "replacement-model"
			if err = m.UpdateRunConfig(id, updated); err != nil {
				t.Fatal(err)
			}
			if err = m.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			m = NewManager(c, ctx)
			defer m.Stop(ctx)
			if err = m.UseDAGTaskProfile(id, "done"); err == nil {
				t.Fatal("completed task accepted a route change")
			}
			if tc.useProfile {
				if err = m.UseDAGTaskProfile(id, "retry"); err != nil {
					t.Fatal(err)
				}
				changed, err := m.Snapshot(id)
				if err != nil || changed.DAG.Tasks[1].Status != "failed" || changed.DAG.Tasks[1].Worktree == nil || changed.DAG.Tasks[1].Worker != oldWorker {
					t.Fatal("changing routing destroyed failed work or its provenance")
				}
				if err = m.Stop(ctx); err != nil {
					t.Fatal(err)
				}
				m = NewManager(c, ctx)
				defer m.Stop(ctx)
			}
			if err = m.ResetDAGTask(id, "retry"); err != nil {
				t.Fatal(err)
			}
			if err = m.ExecuteDAG(id, nil); err != nil {
				t.Fatal(err)
			}
			review := waitGraph(t, m, id, "review")
			calls.mu.Lock()
			got := append([]WorkerConfig(nil), calls.workers...)
			calls.mu.Unlock()
			if len(got) != 2 || got[1].CLI != tc.wantCLI || got[1].Model != tc.wantModel {
				t.Fatalf("retry kept a stale route: %+v", got)
			}
			if review.DAG.Tasks[0].Review != "accepted" || review.DAG.Tasks[0].Result.WorkerConfig.Model != "historical-model" || review.DAG.Tasks[1].Result.WorkerConfig.Model != tc.wantModel {
				t.Fatalf("saved results/provenance changed: %+v", review.DAG.Tasks)
			}
			var oldAttempt bool
			for _, event := range review.Events {
				oldAttempt = oldAttempt || event.TaskID == "retry" && event.Kind == "failed" && event.CLI == oldWorker.CLI && event.Model == oldWorker.Model
			}
			if !oldAttempt {
				t.Fatal("original failed invocation route was relabelled")
			}
		})
	}
}

func TestLegacyTaskRequestsPreserveUnattemptedOverridesAndSupportRuntimeChanges(t *testing.T) {
	original := testConfig(t)
	_, native, err := resolveTaskProfile(original, WorkerConfig{ProfileID: Middle})
	if err != nil {
		t.Fatal(err)
	}
	explicit := WorkerConfig{ProfileID: Middle, Runtime: "cli", CLI: "opencode", Model: "pinned-model"}
	run := Run{DAG: &DAG{Tasks: []DAGTask{
		{ID: "unattempted", Description: "Pending override", Worker: explicit, Status: "pending"},
		{ID: "attempted", Description: "Failed inherited route", Worker: native, Status: "failed"},
		{ID: "explicit-defaults", Description: "Pending explicit local route", Worker: native, Status: "pending"},
	}}}
	restoreTaskRequests(&run, original)
	if run.DAG.Tasks[0].requestedWorker() != explicit {
		t.Fatal("unattempted explicit routing was changed during restore")
	}
	if request := run.DAG.Tasks[2].requestedWorker(); request.Model != native.Model || request.Endpoint != native.Endpoint || request.Runtime != "native" {
		t.Fatal("an unattempted explicit route was mistaken for a resolved attempt")
	}
	updated := original
	updated.Profiles = append([]Profile(nil), original.Profiles...)
	updated.Profiles[1].Runtime, updated.Profiles[1].CLI = "cli", "codex"
	updated.Profiles[1].Model, updated.Profiles[1].Endpoint, updated.Profiles[1].MaxOutputTokens = "replacement-model", "", 0
	if err := ValidateDAG(run.DAG.Tasks, updated); err != nil {
		t.Fatal(err)
	}
	profile, _, err := resolveTaskProfile(updated, run.DAG.Tasks[1].requestedWorker())
	if err != nil || profile.Runtime != "cli" || profile.CLI != "codex" || profile.Endpoint != "" || run.DAG.Tasks[1].Worker != native {
		t.Fatalf("runtime change reused or relabelled a legacy attempt: %+v (%v)", profile, err)
	}
}
