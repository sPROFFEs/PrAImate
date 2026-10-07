package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
)

type waitingWorkerAdapter struct {
	started chan struct{}
	release chan struct{}
}

func TestStudioDAGAndRemoteKnowledgeConfigurationRPC(t *testing.T) {
	s, _ := fixture(t)
	_, err := s.core.ImportAgentYAML(context.Background(), []byte("schema: praimate.agent/v1\nid: remote-rpc\nname: Remote\ninstructions: Use source references.\nsupports: [codex]\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	rpcOK(t, s, "agents.knowledge.config.save", map[string]any{"id": "remote-rpc", "config": core.AgentKnowledgeConfig{Source: "remote", Endpoint: "https://example.test/knowledge"}, "apiKey": "private-rpc-key", "mode": "rag"})
	configResult := rpcOK(t, s, "agents.knowledge.config.get", map[string]string{"id": "remote-rpc"})
	raw, _ := json.Marshal(configResult)
	if strings.Contains(string(raw), "private-rpc-key") || !strings.Contains(string(raw), `"hasAPIKey":true`) || !strings.Contains(string(raw), `"mode":"rag"`) {
		t.Fatalf("config RPC leaked/forgot credential: %s", raw)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git required for parallel planning")
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = s.session.Workspace
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.test", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.test")
		if body, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, body, err)
		}
	}
	git("init")
	_ = os.WriteFile(filepath.Join(s.session.Workspace, "a.go"), []byte("base\n"), 0600)
	git("add", ".")
	git("-c", "core.hooksPath="+os.DevNull, "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	adapter := &planOnlyAdapter{}
	old, _ := core.GetCLIAdapter("codex")
	core.RegisterCLIAdapter(adapter)
	defer func() {
		if old != nil {
			core.RegisterCLIAdapter(old)
		} else {
			core.UnregisterCLIAdapter("codex")
		}
	}()
	config := orchestrator.Config{}
	for _, tier := range []orchestrator.Tier{orchestrator.Primary, orchestrator.Middle, orchestrator.Fast} {
		config.Profiles = append(config.Profiles, orchestrator.Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "selected-" + string(tier), TimeoutSeconds: 10, MaxInputBytes: 32768})
	}
	id := rpcOK(t, s, "workers.plan", map[string]any{"task": "Inspect source", "config": config, "maxParallel": 2}).(string)
	var snapshot orchestrator.Run
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot = rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
		if snapshot.Status == "draft" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.Status != "draft" {
		t.Fatalf("plan: %+v", snapshot)
	}
	savedConfig := orchestrator.Config{Workspace: snapshot.Workspace, Profiles: append([]orchestrator.Profile(nil), snapshot.Profiles...)}
	savedConfig.Profiles[0].TimeoutSeconds = 0
	rpcOK(t, s, "workers.run.config", map[string]any{"id": id, "config": savedConfig})
	updatedRun := rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
	if updatedRun.Profiles[0].TimeoutSeconds != 0 || updatedRun.DAG.Tasks[0].ID != snapshot.DAG.Tasks[0].ID {
		t.Fatal("run settings changed task identity or lost timeout")
	}
	snapshot.DAG.Tasks[0].Worker.Model = "task-specific-model"
	rpcOK(t, s, "workers.graph.save", map[string]any{"id": id, "tasks": snapshot.DAG.Tasks, "maxParallel": 2})
	editedRun := rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
	if editedRun.DAG.Tasks[0].RequestedWorker == nil || editedRun.DAG.Tasks[0].RequestedWorker.Model != "task-specific-model" {
		t.Fatal("draft edits were shadowed by the previous requested route")
	}
	rpcOK(t, s, "workers.graph.profile", map[string]string{"id": id, "taskID": snapshot.DAG.Tasks[0].ID})
	profileRun := rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
	if profileRun.Status != "draft" || profileRun.DAG.Tasks[0].RequestedWorker == nil || profileRun.DAG.Tasks[0].RequestedWorker.Model != "" || profileRun.DAG.Tasks[0].Worker.Model != "" {
		t.Fatal("using a profile did not remove the override, or executed a task")
	}
	rpcOK(t, s, "workers.graph.execute", map[string]string{"id": id})
	for time.Now().Before(deadline) {
		snapshot = rpcOK(t, s, "workers.get", map[string]string{"id": id}).(orchestrator.Run)
		if snapshot.Status == "review" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if snapshot.Status != "review" || snapshot.DAG.Tasks[0].Result == nil {
		t.Fatalf("execution RPC: %+v", snapshot)
	}
	page := rpcOK(t, s, "workers.activity", map[string]any{"id": id, "limit": 2}).(orchestrator.ActivityPage)
	if len(page.Events) != 2 || !page.HasMore {
		t.Fatalf("activity pagination: %+v", page)
	}
	older := rpcOK(t, s, "workers.activity", map[string]any{"id": id, "before": page.Before, "limit": 200}).(orchestrator.ActivityPage)
	if len(older.Events) == 0 || older.Events[len(older.Events)-1].Sequence >= page.Events[0].Sequence {
		t.Fatal("activity cursor repeated newer events")
	}
	preview := rpcOK(t, s, "workers.task.preview", map[string]string{"id": id, "taskID": "a"}).(orchestrator.TaskPreview)
	if preview.Path != snapshot.DAG.Tasks[0].Worktree.Path {
		t.Fatal("preview returned another worktree")
	}
	if response := s.dispatch(RPCRequest{Method: "workers.graph.retry", Params: map[string]string{"id": id, "taskID": "a"}}); response.Error == nil {
		t.Fatal("completed task accepted a retry")
	}
	rpcOK(t, s, "workers.graph.review", map[string]string{"id": id, "taskID": "a", "decision": "rejected"})
	rpcOK(t, s, "workers.graph.cleanup", map[string]string{"id": id})
}

type planOnlyAdapter struct{}

func (*planOnlyAdapter) Name() string                    { return "codex" }
func (*planOnlyAdapter) Available(context.Context) error { return nil }
func (*planOnlyAdapter) SupportsResume() bool            { return false }
func (*planOnlyAdapter) ManagedSafeMode() bool           { return true }
func (*planOnlyAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("unexpected resume")
}
func (*planOnlyAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	if strings.Contains(opts.SystemPrompt, "coordinate a parallel software task graph") {
		return &core.Reply{Text: `{"tasks":[{"id":"a","description":"Inspect source","worker":{"profile":"fast"}}]}`}, nil
	}
	return &core.Reply{Text: `{"action":"final","content":"inspected"}`}, nil
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
	for _, scope := range []string{"worker-test", "workers-test"} {
		t.Run(scope, func(t *testing.T) {
			root, _ := fixture(t)
			result := make(chan bool, 1)
			go func() {
				allowed, err := root.approvalProvider(scope).Request(root.ctx, "command.run", map[string]any{"command": "go"})
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
		})
	}
}
