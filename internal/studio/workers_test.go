package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/orchestrator"
)

type waitingWorkerAdapter struct {
	started chan struct{}
	release chan struct{}
}

func (*waitingWorkerAdapter) Name() string                    { return "codex" }
func (*waitingWorkerAdapter) Available(context.Context) error { return nil }
func (*waitingWorkerAdapter) SupportsResume() bool            { return false }
func (*waitingWorkerAdapter) ManagedSafeMode() bool           { return true }
func (*waitingWorkerAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("resume not expected")
}
func (a *waitingWorkerAdapter) SingleShot(ctx context.Context, _ core.SingleShotOpts) (*core.Reply, error) {
	select {
	case a.started <- struct{}{}:
	default:
	}
	select {
	case <-a.release:
		return &core.Reply{Text: `{"action":"final","content":"survived reconnect"}`}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type orchestratedTestAdapter struct {
	mu     sync.Mutex
	models []string
}

func (*orchestratedTestAdapter) Name() string                    { return "codex" }
func (*orchestratedTestAdapter) Available(context.Context) error { return nil }
func (*orchestratedTestAdapter) SupportsResume() bool            { return false }
func (*orchestratedTestAdapter) ManagedSafeMode() bool           { return true }
func (*orchestratedTestAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("resume not expected")
}
func (a *orchestratedTestAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.models = append(a.models, opts.Model)
	switch len(a.models) {
	case 1:
		return &core.Reply{Text: `{"action":"delegate","tier":"middle","task":"small task"}`}, nil
	case 2:
		return &core.Reply{Text: `{"action":"final","content":"middle answer"}`}, nil
	default:
		return &core.Reply{Text: `{"action":"final","content":"primary answer"}`}, nil
	}
}

func TestStudioWorkersRPCUsesIndependentModels(t *testing.T) {
	s, _ := fixture(t)
	adapter := &orchestratedTestAdapter{}
	old, _ := core.GetCLIAdapter(adapter.Name())
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter(adapter.Name())
		}
	})
	config := orchestrator.Config{Workspace: s.session.Workspace}
	for _, tier := range []orchestrator.Tier{orchestrator.Primary, orchestrator.Middle, orchestrator.Fast} {
		config.Profiles = append(config.Profiles, orchestrator.Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "model-" + string(tier), TimeoutSeconds: 10, MaxInputBytes: 32768})
	}
	config.Profiles[0].AllowCommands = true
	rpcOK(t, s, "workers.config.save", config)
	got := rpcOK(t, s, "workers.config.get", nil).(orchestrator.Config)
	if got.Profiles[0].Model != "model-primary" || !got.Profiles[0].AllowCommands {
		t.Fatalf("saved primary profile=%+v", got.Profiles[0])
	}
	id := rpcOK(t, s, "workers.start", map[string]any{"task": "main task", "config": config}).(string)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run := rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
		if run.Status == "completed" {
			if run.Result != "primary answer" || len(run.Events) == 0 {
				t.Fatalf("run=%+v", run)
			}
			adapter.mu.Lock()
			models := append([]string(nil), adapter.models...)
			adapter.mu.Unlock()
			want := []string{"model-primary", "model-middle", "model-primary"}
			if len(models) != len(want) {
				t.Fatalf("models=%v", models)
			}
			for i := range want {
				if models[i] != want[i] {
					t.Fatalf("models=%v", models)
				}
			}
			rpcOK(t, s, "workers.continue", map[string]string{"id": id, "task": "follow up"})
			followDeadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(followDeadline) {
				follow := rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
				if follow.Status == "completed" && len(follow.Turns) == 2 {
					if follow.Turns[1].Task != "follow up" || follow.Result != "primary answer" {
						t.Fatalf("follow-up=%+v", follow)
					}
					reopened := orchestrator.NewManager(s.core, context.Background())
					restored, err := reopened.Snapshot(id)
					if err != nil || len(restored.Turns) != 2 || restored.Result != "primary answer" || len(restored.Events) == 0 || restored.Workspace != config.Workspace || restored.Profiles[0].Model != "model-primary" {
						t.Fatalf("reopened worker chat=%+v error=%v", restored, err)
					}
					if err := reopened.Continue(id, "after restart"); err != nil {
						t.Fatalf("continue reopened worker chat: %v", err)
					}
					resumeDeadline := time.Now().Add(2 * time.Second)
					for time.Now().Before(resumeDeadline) {
						resumed, err := reopened.Snapshot(id)
						if err != nil {
							t.Fatal(err)
						}
						if resumed.Status == "completed" && len(resumed.Turns) == 3 {
							break
						}
						time.Sleep(10 * time.Millisecond)
					}
					resumed, err := reopened.Snapshot(id)
					if err != nil || resumed.Status != "completed" || len(resumed.Turns) != 3 || resumed.Turns[2].Task != "after restart" {
						t.Fatalf("continued reopened worker chat=%+v error=%v", resumed, err)
					}
					rpcOK(t, s, "workers.rename", map[string]string{"id": id, "title": "Renamed worker chat"})
					reopened = orchestrator.NewManager(s.core, context.Background())
					renamed, err := reopened.Snapshot(id)
					if err != nil || renamed.Title != "Renamed worker chat" || renamed.Task != "main task" {
						t.Fatalf("renamed worker chat=%+v error=%v", renamed, err)
					}
					rpcOK(t, s, "workers.delete", map[string]string{"id": id})
					if _, err := orchestrator.NewManager(s.core, context.Background()).Snapshot(id); err == nil {
						t.Fatal("deleted worker chat was restored")
					}
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Fatal("worker follow-up did not complete")
		}
		if run.Status == "failed" {
			t.Fatalf("run failed: %s", run.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker run did not complete")
}

func TestWorkerHistoryRestoresAllChatsAndInterruptedActivity(t *testing.T) {
	s, _ := fixture(t)
	config := orchestrator.Config{Workspace: s.session.Workspace}
	for _, tier := range []orchestrator.Tier{orchestrator.Primary, orchestrator.Middle, orchestrator.Fast} {
		config.Profiles = append(config.Profiles, orchestrator.Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "test-" + string(tier), TimeoutSeconds: 10, MaxInputBytes: 32768})
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 51 {
		id := fmt.Sprintf("worker-history-%02d", i)
		if _, err := s.core.CreateChat(context.Background(), core.CreateChatRequest{ID: id, Title: fmt.Sprintf("Saved task %d", i), CLIAgent: "workers", WorkspacePath: config.Workspace, Settings: core.ChatSettings{Surface: "workers"}}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.core.AddMessage(context.Background(), id, "system", string(configJSON), nil); err != nil {
			t.Fatal(err)
		}
		run := orchestrator.Run{ID: id, Title: fmt.Sprintf("Saved task %d", i), Task: "original task", Workspace: config.Workspace, CurrentTask: "original task", Status: "running", Profiles: config.Profiles, Events: []orchestrator.Event{{Tier: orchestrator.Primary, Kind: "request", Text: "started"}}}
		runJSON, err := json.Marshal(run)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.core.AddMessage(context.Background(), id, "assistant", string(runJSON), nil); err != nil {
			t.Fatal(err)
		}
	}
	reopened := orchestrator.NewManager(s.core, context.Background())
	list, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(list); got < 51 {
		t.Fatalf("restored %d worker chats, want at least 51", got)
	}
	run, err := reopened.Snapshot("worker-history-00")
	if err != nil || run.Status != "cancelled" || run.Title != "Saved task 0" || len(run.Events) != 1 || len(run.Turns) != 1 || !strings.Contains(run.Turns[0].Error, "Last recorded activity") {
		t.Fatalf("interrupted worker chat=%+v error=%v", run, err)
	}
}

func TestStudioWorkerSurvivesConnectionAndIsVisibleToNextConnection(t *testing.T) {
	root, _ := fixture(t)
	adapter := &waitingWorkerAdapter{started: make(chan struct{}, 1), release: make(chan struct{})}
	old, _ := core.GetCLIAdapter(adapter.Name())
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter(adapter.Name())
		}
	})
	config := orchestrator.Config{Workspace: root.session.Workspace}
	for _, tier := range []orchestrator.Tier{orchestrator.Primary, orchestrator.Middle, orchestrator.Fast} {
		config.Profiles = append(config.Profiles, orchestrator.Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "test-" + string(tier), TimeoutSeconds: 10, MaxInputBytes: 32768})
	}
	first := root.newConnectionServer()
	first.session.Workspace = root.session.Workspace
	if first.workers != root.workers {
		t.Fatal("Studio connection did not share the root worker manager")
	}
	rpcOK(t, first, "workers.config.save", config)
	id := rpcOK(t, first, "workers.start", map[string]string{"task": "long task"}).(string)
	select {
	case <-adapter.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	_ = first.Close()
	second := root.newConnectionServer()
	defer second.Close()
	second.session.Workspace = root.session.Workspace
	if run := rpcOK(t, second, "workers.get", map[string]string{"id": id}).(orchestrator.Run); run.Status != "running" {
		t.Fatalf("worker after reconnect: %+v", run)
	}
	close(adapter.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run := rpcOK(t, second, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
		if run.Status == "completed" {
			if run.Result != "survived reconnect" {
				t.Fatalf("result=%q", run.Result)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not finish after reconnect")
}

func TestWorkerApprovalCanBeAnsweredAfterReconnect(t *testing.T) {
	root, _ := fixture(t)
	result := make(chan bool, 1)
	go func() {
		allowed, err := root.approvalProvider("worker-test").Request(root.ctx, "command.run", map[string]any{"command": "go"})
		result <- allowed && err == nil
	}()
	var id string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		root.mu.Lock()
		for key := range root.approvals {
			id = key
		}
		root.mu.Unlock()
		if id != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if id == "" {
		t.Fatal("worker approval was not recorded")
	}
	child := root.newConnectionServer()
	defer child.Close()
	var output strings.Builder
	child.writer = &output
	root.rebroadcastWorkerApprovals(child)
	var notification RPCNotification
	if err := json.Unmarshal([]byte(output.String()), &notification); err != nil || notification.Method != "run.approval.required" {
		t.Fatalf("reconnected approval notification=%s error=%v", output.String(), err)
	}
	rpcOK(t, child, "runs.approve", map[string]any{"approvalId": id})
	select {
	case allowed := <-result:
		if !allowed {
			t.Fatal("approval was not delivered to the running worker")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("approval remained blocked after reconnect")
	}
}
