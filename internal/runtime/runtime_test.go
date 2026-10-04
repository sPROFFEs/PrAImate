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
	"strings"
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

type fakeStreamCLI struct {
	fakeCLI
	events []core.StreamEvent
}

func (f *fakeStreamCLI) SingleShotStream(ctx context.Context, opts core.SingleShotOpts, emit core.StreamHandler) (*core.Reply, error) {
	f.seen = opts
	if f.events == nil {
		emit(core.StreamEvent{Type: "text", Text: "live"})
	}
	for _, event := range f.events {
		emit(event)
	}
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
	if len(nativeProgress) != 2 || nativeProgress[0].Kind != "backend_status" || nativeProgress[1].Text != "scoped answer" {
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
		Model: "different-model", ReasoningEffort: "medium", Task: "task", WorkspaceRoot: t.TempDir(),
		Progress: func(event ProgressEvent) { events = append(events, event) },
	})
	if err != nil || result.Content != "scoped answer" || adapter.seen.Model != "different-model" || adapter.seen.ReasoningEffort != "medium" || len(events) != 1 || events[0].Text != "live" {
		t.Fatalf("result=%+v error=%v model=%q events=%+v", result, err, adapter.seen.Model, events)
	}
}

func TestCLIWorkerPreservesReportedReasoningAndToolLifecycle(t *testing.T) {
	adapter := &fakeStreamCLI{fakeCLI: fakeCLI{safe: true}, events: []core.StreamEvent{
		{Type: "reasoning", Text: "reported plan"}, {Type: "tool_start", Tool: "read", Detail: "main.go"}, {Type: "tool_end", Tool: "read", OK: true}, {Type: "error", Detail: "provider warning"},
	}}
	var events []ProgressEvent
	_, err := (CLI{Adapter: adapter}).Execute(context.Background(), Request{Task: "test", WorkspaceRoot: t.TempDir(), Progress: func(e ProgressEvent) { events = append(events, e) }})
	if err != nil || len(events) != 4 {
		t.Fatalf("missing CLI activity: %+v %v", events, err)
	}
	for i, kind := range []string{"reasoning", "tool_start", "tool_end", "error"} {
		if events[i].Kind != kind {
			t.Fatalf("lost event type: %+v", events[i])
		}
	}
}

func TestCLIWorkerReportsProcessAndTurnStartupBeforeOutput(t *testing.T) {
	adapter := &fakeStreamCLI{fakeCLI: fakeCLI{safe: true}, events: []core.StreamEvent{
		{Type: "status", Detail: "Codex process started."}, {Type: "step_start", Detail: "turn"},
	}}
	var events []ProgressEvent
	_, err := (CLI{Adapter: adapter}).Execute(context.Background(), Request{Task: "Plan", WorkspaceRoot: t.TempDir(), Progress: func(e ProgressEvent) { events = append(events, e) }})
	if err != nil || len(events) != 2 || events[0].Kind != "backend_status" || events[1].Kind != "backend_status" {
		t.Fatalf("no evidence of CLI startup reached the worker: %+v %v", events, err)
	}
}

func TestCLIWorkerPairsToolResultsAndReportsActualModelOnce(t *testing.T) {
	adapter := &fakeStreamCLI{fakeCLI: fakeCLI{safe: true}, events: []core.StreamEvent{
		{Type: "model", Model: "reported-model"}, {Type: "model", Model: "reported-model"},
		{Type: "tool_start", ID: "a", Tool: "Read", Detail: "first.go"},
		{Type: "tool_start", ID: "b", Tool: "Read", Detail: "second.go"},
		{Type: "tool_end", ID: "b", OK: false}, {Type: "tool_end", ID: "a", OK: true},
	}}
	var events []ProgressEvent
	_, err := (CLI{Adapter: adapter}).Execute(context.Background(), Request{Model: "selected-model", Task: "Inspect", WorkspaceRoot: t.TempDir(), Progress: func(e ProgressEvent) { events = append(events, e) }})
	if err != nil || len(events) != 5 || events[0].Kind != "backend_status" || !strings.Contains(events[0].Text, "reported-model") || !strings.Contains(events[3].Text, "Read second.go") || !strings.Contains(events[3].Text, "ok=false") || !strings.Contains(events[4].Text, "Read first.go") {
		t.Fatalf("tool results cannot be associated with their calls, or actual model lost: %+v %v", events, err)
	}
}

func TestWorkerUsageJSONMatchesFrontendAndReadsOlderSnapshots(t *testing.T) {
	raw, err := json.Marshal(Usage{InputTokens: 5, OutputTokens: 3, Source: "provider"})
	if err != nil || string(raw) != `{"inputTokens":5,"outputTokens":3,"source":"provider"}` {
		t.Fatalf("frontend usage contract mismatch: %s %v", raw, err)
	}
	var old Usage
	if err = json.Unmarshal([]byte(`{"InputTokens":5,"OutputTokens":3,"Source":"provider"}`), &old); err != nil || old.InputTokens != 5 || old.Source != "provider" {
		t.Fatalf("old usage lost: %+v %v", old, err)
	}
}

func TestCLIWorkerEditModeUsesManagedAdapterOnly(t *testing.T) {
	adapter := &fakeCLI{safe: true, name: "codex"}
	_, err := (CLI{Adapter: adapter, AllowEdits: true}).Execute(context.Background(), Request{Task: "edit", WorkspaceRoot: t.TempDir()})
	if err != nil || adapter.seen.Tools != "edits" {
		t.Fatalf("edit mode error=%v tools=%q", err, adapter.seen.Tools)
	}
	adapter.name = "copilot"
	if _, err := (CLI{Adapter: adapter, AllowEdits: true}).Execute(context.Background(), Request{Task: "edit", WorkspaceRoot: t.TempDir()}); err != nil || adapter.seen.Tools != "edits" {
		t.Fatalf("Copilot edit mode: %v", err)
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
	limits := make(chan *int, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
		var payload struct {
			Model     string `json:"model"`
			MaxTokens *int   `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Model != "tiny-model" {
			t.Errorf("model=%q error=%v", payload.Model, err)
		}
		limits <- payload.MaxTokens
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
	req := Request{Model: "tiny-model", Task: "work", WorkspaceRoot: t.TempDir(), Limits: Limits{MaxInputBytes: 4096, Timeout: time.Second}}
	result, err := (PraimateCLI{Core: c}).Execute(context.Background(), req)
	if err != nil || result.Content != `{"action":"final","content":"done"}` {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if limit := <-limits; limit != nil {
		t.Fatalf("automatic worker imposed a ceiling for an unknown backend window: %d", *limit)
	}
	req.Limits.MaxOutputTokens = 333
	if _, err := (PraimateCLI{Core: c}).Execute(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if limit := <-limits; limit == nil || *limit != 333 {
		t.Fatalf("explicit worker limit=%v", limit)
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
