package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestFullAccessWorkerRunsCommandsWithoutApproval(t *testing.T) {
	cfg := testConfig(t)
	if err := json.Unmarshal([]byte(`{"accessMode":"full"}`), &cfg); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		command += ".exe"
	}
	action, _ := json.Marshal(map[string]any{"action": "tool", "tool": "command.run", "arguments": map[string]any{"command": command, "args": []string{"version"}}})
	worker := &fakeWorker{responses: []string{string(action), `{"action":"final","content":"checked"}`}}
	approvals := 0
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }, Approval: &core.ApprovalConfig{Request: func(context.Context, string, map[string]any) (bool, error) { approvals++; return false, nil }}}
	got, err := runner.Run(context.Background(), cfg, "Check the compiler")
	if err != nil || got != "checked" || approvals != 0 {
		t.Fatalf("result=%q err=%v approval prompts=%d", got, err, approvals)
	}
	if strings.Contains(worker.requests[0].SystemPrompt, "every command requires user approval") {
		t.Fatal("full access prompt still asks for approval")
	}
}

type automaticRetryAdapter struct {
	dagAdapter
	calls    atomic.Int32
	failures int32
	failure  string
	started  chan struct{}
}

func (a *automaticRetryAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	n := a.calls.Add(1)
	if n == 1 {
		_ = os.WriteFile(filepath.Join(opts.Cwd, "a.go"), []byte("partial\n"), 0600)
		if a.started != nil {
			close(a.started)
		}
	} else {
		body, _ := os.ReadFile(filepath.Join(opts.Cwd, "a.go"))
		if string(body) != "partial\n" {
			return nil, errors.New("retry discarded partial work")
		}
	}
	if n <= a.failures {
		message := a.failure
		if message == "" {
			message = "temporary provider failure"
		}
		return &core.Reply{Text: "partial result"}, errors.New(message)
	}
	return &core.Reply{Text: `{"action":"final","content":"recovered"}`}, nil
}
func TestDAGAutomaticRetriesAreBoundedAndPreserveChanges(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		retries, failures, calls int
	}{{"recovers", 2, 1, 2}, {"exhausted", 2, 8, 3}, {"disabled", 0, 8, 1}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t)
			cfg.Workspace = gitFixture(t)
			raw, _ := json.Marshal(map[string]any{"maxRetries": tc.retries, "retryDelaySeconds": 0})
			_ = json.Unmarshal(raw, &cfg)
			adapter := &automaticRetryAdapter{dagAdapter: dagAdapter{name: "auto-retry"}, failures: int32(tc.failures)}
			core.RegisterCLIAdapter(adapter)
			defer core.UnregisterCLIAdapter(adapter.Name())
			cfg.Profiles[1].Runtime = "cli"
			cfg.Profiles[1].CLI = adapter.Name()
			cfg.Profiles[1].Endpoint = ""
			cfg.Profiles[1].MaxOutputTokens = 0
			st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			c, _ := core.New(core.Options{Store: st})
			ctx := context.Background()
			id := "automatic-retry"
			_, err = c.CreateChat(ctx, core.CreateChatRequest{ID: id, Title: "Retry", WorkspacePath: cfg.Workspace, CLIAgent: "workers", Settings: core.ChatSettings{Surface: "workers"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ = json.Marshal(cfg)
			_, _ = c.AddMessage(ctx, id, "system", string(raw), nil)
			m := NewManager(c, ctx)
			w := WorktreeManager{Workspace: cfg.Workspace, RunID: id}
			base, branch, _ := w.Repository(ctx)
			m.runs[id] = &Run{ID: id, Workspace: cfg.Workspace, Profiles: cfg.Profiles, config: cfg, Status: "draft", DAG: &DAG{BaseCommit: base, TargetBranch: branch, MaxParallel: 1, Tasks: []DAGTask{{ID: "edit", Description: "Finish the file edit", Worker: WorkerConfig{ProfileID: Middle}, Status: "pending"}}}}
			if err = m.ExecuteDAG(id, nil); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var snap Run
			for time.Now().Before(deadline) {
				snap, _ = m.Snapshot(id)
				if snap.Status != "running" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if int(adapter.calls.Load()) != tc.calls {
				t.Fatalf("calls=%d want=%d run=%+v", adapter.calls.Load(), tc.calls, snap)
			}
			if len(snap.Attempts) != tc.calls {
				t.Fatalf("attempt history=%+v", snap.Attempts)
			}
			if tc.failures <= tc.retries && (snap.Status != "review" || snap.DAG.Tasks[0].Output != "recovered") {
				t.Fatalf("recovery failed: %+v", snap)
			}
			restored := NewManager(c, ctx)
			saved, _ := restored.Snapshot(id)
			if len(saved.Attempts) != tc.calls {
				t.Fatal("restart lost automatic retry history")
			}
		})
	}
}

func retryManagerFixture(t *testing.T, adapter core.CLIAdapter) *Manager {
	t.Helper()
	old, _ := core.GetCLIAdapter(adapter.Name())
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter(adapter.Name())
		}
	})
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(c, context.Background())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}
func TestAutomaticRetryFitsEvidenceIntoConfiguredInput(t *testing.T) {
	cfg := testConfig(t)
	adapter := &automaticRetryAdapter{dagAdapter: dagAdapter{name: "retry-budget"}, failures: 1, failure: strings.Repeat("provider interrupted ", 400)}
	p := &cfg.Profiles[0]
	p.Runtime, p.CLI, p.Endpoint, p.MaxOutputTokens = "cli", adapter.Name(), "", 0
	p.MaxInputBytes = len(workerInstructions(cfg, Primary, *p)) + 600
	cfg.MaxRetries = 1
	m := retryManagerFixture(t, adapter)
	id, err := m.StartWithConfigAndApproval("Finish the assignment", cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap := waitGraph(t, m, id, "completed")
	if adapter.calls.Load() != 2 || snap.AutoRetriesUsed != 1 {
		t.Fatalf("retry budget: %+v calls=%d", snap, adapter.calls.Load())
	}
}
func TestStopCancelsDAGAutomaticRetryDelay(t *testing.T) {
	cfg := testConfig(t)
	cfg.Workspace = gitFixture(t)
	cfg.MaxRetries = 2
	cfg.RetryDelaySeconds = 60
	adapter := &automaticRetryAdapter{dagAdapter: dagAdapter{name: "retry-stop"}, failures: 8}
	p := &cfg.Profiles[1]
	p.Runtime, p.CLI, p.Endpoint, p.MaxOutputTokens = "cli", adapter.Name(), "", 0
	m := retryManagerFixture(t, adapter)
	id := "stop-retry"
	ctx := context.Background()
	_, err := m.core.CreateChat(ctx, core.CreateChatRequest{ID: id, Title: "Stop retry", WorkspacePath: cfg.Workspace, CLIAgent: "workers", Settings: core.ChatSettings{Surface: "workers"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cfg)
	_, _ = m.core.AddMessage(ctx, id, "system", string(raw), nil)
	base, branch, err := (WorktreeManager{Workspace: cfg.Workspace, RunID: id}).Repository(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.runs[id] = &Run{ID: id, Workspace: cfg.Workspace, Profiles: cfg.Profiles, config: cfg, MaxRetries: cfg.MaxRetries, Status: "draft", DAG: &DAG{BaseCommit: base, TargetBranch: branch, MaxParallel: 1, Tasks: []DAGTask{{ID: "edit", Description: "Finish the edit", Worker: WorkerConfig{ProfileID: Middle}, Status: "pending"}}}}
	if err = m.ExecuteDAG(id, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, _ := m.Snapshot(id)
		if snap.DAG.Tasks[0].Status == "retrying" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no scheduled retry")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = m.Cancel(id); err != nil {
		t.Fatal(err)
	}
	snap := waitGraph(t, m, id, "cancelled")
	task := snap.DAG.Tasks[0]
	if adapter.calls.Load() != 1 || task.Status != "cancelled" || task.NextRetryAt != nil {
		t.Fatalf("stop reran work: %+v calls=%d", task, adapter.calls.Load())
	}
	body, _ := os.ReadFile(filepath.Join(task.Worktree.Path, "a.go"))
	if string(body) != "partial\n" {
		t.Fatal("stop discarded changes")
	}
}
func TestFullAccessPlanningRemainsReadOnly(t *testing.T) {
	cfg := testConfig(t)
	cfg.AccessMode = "full"
	worker := &fakeWorker{responses: []string{`{"tasks":[{"id":"task","description":"Inspect the project","dependencies":[],"worker":{"profile":"fast"}}]}`}}
	runner := Runner{Resolve: func(profile Profile) (workerruntime.Runtime, error) {
		if profile.FullAccess || profile.AllowEdits || profile.AllowCommands {
			t.Fatal("planning received mutation permissions")
		}
		return worker, nil
	}}
	tasks, err := runner.Plan(context.Background(), cfg, "Plan the changes")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("plan=%+v error=%v", tasks, err)
	}
}
func TestAutomaticRetryHonorsDeniedApprovalAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if canAutomaticallyRetry(ctx, "temporary failure") {
		t.Fatal("cancelled task would retry")
	}
	for _, message := range []string{"command denied by the user", "requires an interactive user approval", "worker is not allowed to edit files"} {
		if canAutomaticallyRetry(context.Background(), message) {
			t.Fatalf("permission failure would retry: %s", message)
		}
	}
	if retryDelay(Config{RetryDelaySeconds: 5}, 10) != 60*time.Second {
		t.Fatal("retry delay exceeds cap")
	}
}
