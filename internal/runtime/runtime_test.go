package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

type fakeCLI struct {
	safe bool
	name string
	seen core.SingleShotOpts
}

type fakeStreamCLI struct{ fakeCLI }

func (f *fakeStreamCLI) SingleShotStream(ctx context.Context, opts core.SingleShotOpts, emit core.StreamHandler) (*core.Reply, error) {
	f.seen = opts
	emit(core.StreamEvent{Type: "text", Text: "live"})
	return &core.Reply{Text: "scoped answer"}, ctx.Err()
}

func (f *fakeCLI) Name() string {
	if f.name != "" {
		return f.name
	}
	return "fake"
}
func (*fakeCLI) Available(context.Context) error { return nil }
func (*fakeCLI) SupportsResume() bool            { return false }
func (*fakeCLI) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("unsupported")
}
func (f *fakeCLI) ManagedSafeMode() bool { return f.safe }
func (f *fakeCLI) SingleShot(ctx context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	f.seen = opts
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &core.Reply{Text: "scoped answer"}, nil
}

func TestNativeAndCLIExecuteSameScopedTask(t *testing.T) {
	root := t.TempDir()
	var wire struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools     []any `json:"tools"`
		MaxTokens int   `json:"max_tokens"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"scoped answer\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3},\"choices\":[]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	task := Request{Model: "test-model", SystemPrompt: "Summarize only the supplied facts.", Task: "Fact: one.", WorkspaceRoot: root,
		Limits: Limits{MaxInputBytes: 256, MaxOutputTokens: 32, Timeout: time.Second}}
	native := Native{Route: core.ChatLocalEndpoint{Endpoint: server.URL}}
	var nativeProgress []ProgressEvent
	task.Progress = func(event ProgressEvent) { nativeProgress = append(nativeProgress, event) }
	result, err := native.Execute(context.Background(), task)
	if err != nil || result.Content != "scoped answer" || result.Usage.Source != "provider" || result.Usage.InputTokens != 12 {
		t.Fatalf("native result = %+v, %v", result, err)
	}
	if len(wire.Messages) != 2 || wire.Messages[0].Role != "system" || wire.Messages[0].Content != task.SystemPrompt || wire.Messages[1].Role != "user" || wire.Messages[1].Content != task.Task || len(wire.Tools) != 0 || wire.MaxTokens != 32 {
		t.Fatalf("native worker received unexpected context or tools: %+v", wire)
	}
	if len(nativeProgress) != 1 || nativeProgress[0].Text != "scoped answer" {
		t.Fatalf("native progress=%+v", nativeProgress)
	}

	adapter := &fakeCLI{safe: true}
	cliTask := task
	cliTask.Limits.MaxOutputTokens = 0 // the CLI adapter cannot enforce this limit
	cliResult, err := (CLI{Adapter: adapter}).Execute(context.Background(), cliTask)
	if err != nil || cliResult.Content != result.Content || cliResult.Usage.Source != "unavailable" {
		t.Fatalf("CLI result = %+v, %v", cliResult, err)
	}
	if adapter.seen.Cwd != root || adapter.seen.Message != task.Task || adapter.seen.SystemPrompt != task.SystemPrompt || adapter.seen.Tools != "" {
		t.Fatalf("CLI worker received wrong scope: %+v", adapter.seen)
	}
}

func TestCLIWorkerForwardsLiveTextAndModel(t *testing.T) {
	adapter := &fakeStreamCLI{fakeCLI: fakeCLI{safe: true}}
	var events []ProgressEvent
	result, err := (CLI{Adapter: adapter}).Execute(context.Background(), Request{
		Model: "different-model", Task: "task", WorkspaceRoot: t.TempDir(),
		Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if err != nil || result.Content != "scoped answer" || adapter.seen.Model != "different-model" || len(events) != 1 || events[0].Text != "live" {
		t.Fatalf("result=%+v error=%v model=%q events=%+v", result, err, adapter.seen.Model, events)
	}
}

func TestCLIWorkerEditModeUsesManagedAdapterOnly(t *testing.T) {
	adapter := &fakeCLI{safe: true, name: "codex"}
	_, err := (CLI{Adapter: adapter, AllowEdits: true}).Execute(context.Background(), Request{Task: "edit", WorkspaceRoot: t.TempDir()})
	if err != nil || adapter.seen.Tools != "edits" {
		t.Fatalf("edit mode error=%v tools=%q", err, adapter.seen.Tools)
	}
	adapter.name = "praimate-code"
	if _, err := (CLI{Adapter: adapter, AllowEdits: true}).Execute(context.Background(), Request{Task: "edit", WorkspaceRoot: t.TempDir()}); err == nil {
		t.Fatal("accepted unsupported CLI edit mode")
	}
}

func TestOpenCodeWorkersKeepCLISafeWhileUsingHostTools(t *testing.T) {
	for _, name := range []string{"opencode", "praimate-code"} {
		adapter := &fakeCLI{safe: true, name: name}
		_, err := (CLI{Adapter: adapter}).Execute(context.Background(), Request{Task: "use host tool", WorkspaceRoot: t.TempDir()})
		if err != nil || adapter.seen.Tools != "plan" {
			t.Fatalf("%s tools=%q error=%v", name, adapter.seen.Tools, err)
		}
	}
}

func TestPraimateCLIWorkerResolvesModelThroughCore(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "tiny-model" {
			t.Errorf("model=%q error=%v", payload.Model, err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"{\\\"action\\\":\\\"final\\\",\\\"content\\\":\\\"done\\\"}\"},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	t.Setenv("OPENAI_BASE_URL", server.URL)
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "test.db"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (PraimateCLI{Core: c}).Execute(context.Background(), Request{Model: "tiny-model", Task: "work", WorkspaceRoot: t.TempDir(), Limits: Limits{MaxInputBytes: 4096, Timeout: time.Second}})
	if err != nil || result.Content != `{"action":"final","content":"done"}` {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestWorkerRuntimeRejectsUnenforcedLimitsAndUnsafeAdapter(t *testing.T) {
	root := t.TempDir()
	req := Request{Task: "short task", WorkspaceRoot: root, Limits: Limits{MaxInputBytes: 3}}
	if _, err := (CLI{Adapter: &fakeCLI{safe: true}}).Execute(context.Background(), req); err == nil {
		t.Fatal("accepted input beyond its byte limit")
	}
	req.Limits.MaxInputBytes = 0
	if _, err := (CLI{Adapter: &fakeCLI{safe: false}}).Execute(context.Background(), req); err == nil {
		t.Fatal("accepted an adapter without enforced safe mode")
	}
	req.Limits.MaxOutputTokens = 10
	if _, err := (CLI{Adapter: &fakeCLI{safe: true}}).Execute(context.Background(), req); err == nil {
		t.Fatal("claimed to enforce unsupported CLI output tokens")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req.Limits.MaxOutputTokens = 0
	if _, err := (CLI{Adapter: &fakeCLI{safe: true}}).Execute(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was lost: %v", err)
	}
}

func TestNativeWorkerTimeoutCancelsProviderRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	req := Request{Model: "test-model", Task: "bounded task", WorkspaceRoot: t.TempDir(),
		Limits: Limits{MaxOutputTokens: 16, Timeout: 20 * time.Millisecond}}
	_, err := (Native{Route: core.ChatLocalEndpoint{Endpoint: server.URL}}).Execute(context.Background(), req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native worker timeout = %v, want deadline exceeded", err)
	}
}
