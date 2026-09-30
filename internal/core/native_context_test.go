package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeAutomaticOutputUsesFreeContextForReasoning(t *testing.T) {
	c := nativeTestCore(t)
	var sent int
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		sent = request.MaxTokens
		if sent <= 1024 {
			return nativeHTTP(nativeSSE("", "length")), nil
		}
		thinking := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Planning the answer\"}}]}\n\n"
		return nativeHTTP(thinking + nativeUsageSSE("Answer after reasoning", 948, 2000)), nil
	})}}
	installNativeTestAdapter(t, a)
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{CLIAgent: "praimate-cli", WorkspacePath: t.TempDir(), Settings: ChatSettings{Local: &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "reasoner", ContextTokens: 8192}}})
	if err != nil {
		t.Fatal(err)
	}
	var budget NativeContextStatus
	reply, err := c.ContinueChatStream(ctx, chat.ID, "Explain the project", chat.WorkspacePath, "", nil, func(e StreamEvent) {
		if e.Type == "context" {
			raw, _ := json.Marshal(e.Raw)
			_ = json.Unmarshal(raw, &budget)
		}
	})
	if err != nil || reply.Reply != "Answer after reasoning" {
		t.Fatalf("reply=%+v error=%v", reply, err)
	}
	if sent <= 1024 || sent+budget.EstimatedInput+budget.SafetyReserve > budget.Window || budget.LastOutputLimit != sent {
		t.Fatalf("unsafe or undersized automatic output: sent=%d budget=%+v", sent, budget)
	}
	status, err := c.NativeContext(ctx, chat.ID)
	if err != nil || status == nil || !status.OutputAutomatic || status.LastOutputLimit != sent || status.LastUsage == nil || status.LastUsage.CompletionTokens != 2000 {
		t.Fatalf("missing automatic mode/last request: %+v %v", status, err)
	}
	dashboard, err := c.UsageDashboard(ctx, "")
	if err != nil || dashboard.Totals.Runs != 1 || dashboard.Totals.ReportedRuns != 1 || dashboard.Totals.Tokens != 2948 {
		t.Fatalf("chat usage not recorded exactly once: %+v %v", dashboard, err)
	}
}

func TestNativeAutomaticOutputRejectionReducesAllowanceWithoutDroppingTask(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.OutputAutomatic = true
	var limits []int
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		var request struct {
			MaxTokens int             `json:"max_tokens"`
			Messages  []nativeMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		limits = append(limits, request.MaxTokens)
		if len(limits) == 1 {
			response := nativeHTTP(`{"error":"context_length_exceeded"}`)
			response.StatusCode = 400
			return response, nil
		}
		if len(limits) != 2 || request.Messages[len(request.Messages)-1].Content != "Keep this complete request" {
			t.Fatal("replayed request or lost current task")
		}
		return nativeHTTP(nativeSSE("done", "stop")), nil
	})}}
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: "Keep this complete request"})
	if err != nil || reply.Text != "done" || len(limits) != 2 || limits[1] >= limits[0] || limits[1] < run.local.OutputTokens {
		t.Fatalf("limits=%v reply=%+v error=%v", limits, reply, err)
	}
}

func TestNativeOutputAllocationRespectsContextAndExplicitLimits(t *testing.T) {
	for _, tc := range []struct{ window, input, fixed, want int }{
		{8192, 948, 0, 6835}, {8192, 6500, 0, 1283},
		{32768, 2000, 0, 29130}, {8192, 948, 1024, 1024},
	} {
		route := ChatLocalEndpoint{ContextTokens: tc.window, OutputTokens: autoOutputTokens(tc.window, tc.fixed), OutputAutomatic: tc.fixed == 0}
		status := nativeContextBudget(route, NativeContextStatus{})
		status.EstimatedInput = tc.input
		if got := nativeOutputLimit(route, status); got != tc.want {
			t.Fatalf("%+v: got %d", tc, got)
		}
	}
}

func TestNativeExplicitHostOutputIsNotAutomatic(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	if _, err := c.SaveLocalHost(ctx, LocalHost{ID: "fixed", Endpoint: "http://local.test/v1", ContextTokens: 8192, OutputTokens: 1024}); err != nil {
		t.Fatal(err)
	}
	route, err := c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "m"}, "")
	if err != nil || route.OutputAutomatic || route.OutputTokens != 1024 {
		t.Fatalf("host override lost: %+v %v", route, err)
	}
}

func TestNativeReasoningLimitExplainsMissingAnswer(t *testing.T) {
	requests := 0
	p := nativeProvider{route: nativeTestRun(nil).local, http: &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nativeHTTP("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"Still thinking\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n"), nil
	})}}
	_, err := p.turn(context.Background(), []nativeMessage{{Role: "user", Content: "explain"}}, nil, nil)
	if !errors.Is(err, errNativeOutputLimit) || !strings.Contains(err.Error(), "reasoning consumes") || !strings.Contains(err.Error(), "before producing an answer") || requests != 1 {
		t.Fatalf("misleading error or replayed partial stream: %v requests=%d", err, requests)
	}
}

func nativeUsageSSE(text string, input, output int) string {
	raw, _ := json.Marshal(map[string]any{"choices": []any{}, "usage": NativeUsage{input, output, input + output}})
	return strings.Replace(nativeSSE(text, "stop"), "data: [DONE]", "data: "+string(raw)+"\n\ndata: [DONE]", 1)
}

func TestNativeUsageAndOptionalStreamOptions(t *testing.T) {
	requests := 0
	p := nativeProvider{route: nativeTestRun(nil).local, http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if request["stream_options"] == nil {
				t.Fatal("did not request usage")
			}
			response := nativeHTTP(`{"error":"unknown field stream_options"}`)
			response.StatusCode = 400
			return response, nil
		}
		if request["stream_options"] != nil {
			t.Fatal("retried rejected stream_options")
		}
		return nativeHTTP(nativeUsageSSE("OK", 1500, 27)), nil
	})}}
	for range 2 {
		message, err := p.turn(context.Background(), []nativeMessage{{Role: "user", Content: "hello"}}, nil, nil)
		if err != nil || message.Usage == nil || *message.Usage != (NativeUsage{1500, 27, 1527}) {
			t.Fatalf("usage=%+v err=%v", message.Usage, err)
		}
	}
	if requests != 3 {
		t.Fatalf("unexpected retries: %d", requests)
	}
}

func TestNativeUsageFallbackDoesNotRetryOtherErrors(t *testing.T) {
	requests := 0
	p := nativeProvider{route: nativeTestRun(nil).local, http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		response := nativeHTTP(`{"error":"vision unsupported"}`)
		response.StatusCode = 400
		return response, nil
	})}}
	if _, err := p.turn(context.Background(), nil, nil, nil); err == nil || requests != 1 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

func TestNativeBudgetCountsImagesToolsUnicodeAndReserves(t *testing.T) {
	status := nativeContextBudget(ChatLocalEndpoint{Model: "vision", ContextTokens: 8192, OutputTokens: 2048}, NativeContextStatus{})
	if status.InputLimit != 5735 || status.SafetyReserve != 409 {
		t.Fatalf("bad reservations: %+v", status)
	}
	message := nativeMessage{Role: "user", Content: "Describe it", Images: []nativeImage{{DataURL: strings.Repeat("a", 100), Width: 64, Height: 64}}}
	before := nativeMessageTokens([]nativeMessage{message})
	message.Images[0].DataURL = strings.Repeat("a", 1<<20)
	if after := nativeMessageTokens([]nativeMessage{message}); after != before || after < 1024 {
		t.Fatalf("counted base64 as tokens: %d -> %d", before, after)
	}
	if nativeTextTokens("日本語") < 3 || nativeTextTokens("😀😀") < 2 || nativeTextTokens("{}[]:,") != 6 {
		t.Fatal("Unicode/code punctuation undercounted")
	}
	withCalls := nativeMessage{Role: "assistant", ToolCalls: []nativeToolCall{nativeCall("c", "read_file", `{"path":"test.txt"}`)}}
	if nativeMessageTokens([]nativeMessage{withCalls}) <= nativeMessageTokens([]nativeMessage{{Role: "assistant"}}) {
		t.Fatal("tool calls ignored")
	}
	status.observe(&NativeUsage{PromptTokens: 2000, CompletionTokens: 12}, 1000)
	if status.Calibration != 2.2 {
		t.Fatalf("missing calibration: %+v", status)
	}
	status.observe(&NativeUsage{PromptTokens: 500}, 1000)
	if status.Calibration != 2.2 {
		t.Fatal("underestimated sample weakened safety")
	}
	if changed := nativeContextBudget(ChatLocalEndpoint{Model: "different", ContextTokens: 8192, OutputTokens: 2048}, status); changed.Calibration != 1 || changed.LastUsage != nil {
		t.Fatal("model switch kept old usage calibration")
	}
}

func TestNativeContextCompactionKeepsLatestImageAndToolEvidence(t *testing.T) {
	latest := nativeImage{Name: "latest.png", DataURL: "data:image/png;base64,latest", Width: 1, Height: 1}
	messages := []nativeMessage{
		{Role: "system", Content: "rules"}, {Role: "user", Content: "old", Images: []nativeImage{{Width: 4096, Height: 4096}}}, {Role: "assistant", Content: "old answer"},
		{Role: "user", Content: "latest request", Images: []nativeImage{latest}},
		{Role: "assistant", ToolCalls: []nativeToolCall{nativeCall("r", "read_file", `{"path":"x"}`)}},
		{Role: "tool", ToolCallID: "r", Content: "HEAD evidence\n" + strings.Repeat("noise ", 5000) + "\nTAIL evidence"},
	}
	compacted, changed, err := compactNativeContext(messages, 2600, nativeMessageTokens)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if err := validateNativeMessages(compacted); err != nil {
		t.Fatal(err)
	}
	if nativeMessageTokens(compacted) > 2600 {
		t.Fatal("over budget")
	}
	if len(compacted) != 4 || len(compacted[1].Images) != 1 || compacted[1].Images[0] != latest {
		t.Fatalf("latest request/image or latest exchange discarded: %+v", compacted)
	}
	if !strings.Contains(compacted[3].Content, "HEAD evidence") || !strings.Contains(compacted[3].Content, "TAIL evidence") {
		t.Fatal("tool output evidence lost")
	}
	if len(messages[5].Content) < 20_000 {
		t.Fatal("mutated caller's history")
	}
}

func TestNativeContextBudgetPreventsOversizedRequest(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextTokens, run.local.OutputTokens = 2048, 1024
	called := false
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		called = true
		return nativeHTTP(nativeSSE("oops", "stop")), nil
	})}}
	_, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: strings.Repeat("text ", 4000)})
	if err == nil || !strings.Contains(err.Error(), "context") || called {
		t.Fatalf("sent oversized request: called=%v err=%v", called, err)
	}
}

func TestNativeCalibratedBudgetPreventsOversizedFollowup(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.modelOnly = true
	requests := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
		requests++
		return nativeHTTP(nativeUsageSSE("ok", 6000, 10)), nil
	})}}
	ctx := withNativeExecution(context.Background(), run)
	root := t.TempDir()
	first, err := a.SingleShot(ctx, SingleShotOpts{Cwd: root, Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Resume(ctx, first.SessionID, ResumeOpts{Cwd: root, Message: strings.Repeat("requirement ", 100)})
	if err == nil || !strings.Contains(err.Error(), "context") || requests != 1 {
		t.Fatalf("calibrated budget ignored: requests=%d error=%v", requests, err)
	}
}

func TestNativeOutputLimitKeepsPartialAnswerWithoutExecutingCalls(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(*http.Request) (*http.Response, error) {
		return nativeHTTP(nativeSSE("partial answer", "length", nativeCall("unfinished", "write_file", `{"path":"never.txt","content":"must not run"}`))), nil
	})}}
	root := t.TempDir()
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: root, Message: "work", Tools: "edits"})
	if err == nil || !strings.Contains(err.Error(), "output token limit") {
		t.Fatalf("missing truncation error: %v", err)
	}
	raw, err := c.GetSetting(context.Background(), ScopeCLI, "native.session."+reply.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	var saved nativeSession
	if err := json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	last := saved.Messages[len(saved.Messages)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "partial answer") || !strings.Contains(last.Content, "Incomplete answer") || len(last.ToolCalls) != 0 {
		t.Fatalf("partial response not retained safely: %+v", last)
	}
	if _, err := os.Stat(filepath.Join(root, "never.txt")); !os.IsNotExist(err) {
		t.Fatal("executed tool from truncated output")
	}
}

func TestNativeChatLimitsCanBeRaisedWithoutChangingOtherChats(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	nextID := 0
	newChat := func(cli string) string {
		nextID++
		chat, err := c.CreateChat(ctx, CreateChatRequest{ID: fmt.Sprintf("native-limits-%d", nextID), CLIAgent: cli, WorkspacePath: t.TempDir(), Settings: ChatSettings{Local: &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "model"}}})
		if err != nil {
			t.Fatal(err)
		}
		return chat.ID
	}
	first, second := newChat("praimate-cli"), newChat("praimate-cli")
	if err := c.SetNativeChatLimits(ctx, first, 32768, 2048); err != nil {
		t.Fatal(err)
	}
	status, err := c.NativeContext(ctx, first)
	if err != nil || status.Window != 32768 || status.OutputReserve != 2048 {
		t.Fatalf("first chat context=%+v error=%v", status, err)
	}
	other, err := c.NativeContext(ctx, second)
	if err != nil || other.Window != 8192 || other.OutputReserve != 1024 {
		t.Fatalf("second chat context=%+v error=%v", other, err)
	}
	defaultRoute, err := c.CreateChat(ctx, CreateChatRequest{ID: "native-limits-default", CLIAgent: "praimate-cli", WorkspacePath: t.TempDir(), Settings: ChatSettings{Model: "model"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetNativeChatLimits(ctx, defaultRoute.ID, 32768, 0); err != nil {
		t.Fatalf("override without a per-chat endpoint: %v", err)
	}
	defaultStatus, err := c.NativeContext(ctx, defaultRoute.ID)
	if err != nil || defaultStatus.Window != 32768 {
		t.Fatalf("default route context=%+v error=%v", defaultStatus, err)
	}
	if err := c.SetNativeChatLimits(ctx, first, 1024, 0); err == nil {
		t.Fatal("accepted an undersized context window")
	}
	if err := c.SetNativeChatLimits(ctx, newChat("codex"), 32768, 2048); err == nil {
		t.Fatal("accepted a native context override for another CLI")
	}
}

func TestNativeContextUsagePersistsAndEmits(t *testing.T) {
	c := nativeTestCore(t)
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) { return nativeHTTP(nativeUsageSSE("OK", 1400, 35)), nil })}}
	installNativeTestAdapter(t, a)
	chat, err := c.CreateChat(context.Background(), CreateChatRequest{CLIAgent: "praimate-cli", WorkspacePath: t.TempDir(), Settings: ChatSettings{Local: &ChatLocalEndpoint{Endpoint: "http://local.test/v1", Model: "model", ContextTokens: 8192, OutputTokens: 1024}}})
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]StreamEvent{}
	_, err = c.ContinueChatStream(context.Background(), chat.ID, "hello", chat.WorkspacePath, "rules", nil, func(e StreamEvent) { events[e.Type] = e })
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.NativeContext(context.Background(), chat.ID)
	if err != nil || status.LastUsage == nil || status.LastUsage.PromptTokens != 1400 || status.EstimatedInput <= 0 || status.EstimatedInput > status.InputLimit {
		t.Fatalf("bad saved status: %+v %v", status, err)
	}
	if events["usage"].Raw["last_usage"] == nil || events["context"].Raw["input_limit_tokens"] == nil {
		t.Fatal("missing structured usage/context events")
	}
}

func TestNativeContextRejectionRetriesWithoutReplayingTools(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextTokens = 32768
	root := t.TempDir()
	requests, executed := 0, 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		var request struct {
			Messages []nativeMessage `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		switch requests {
		case 1:
			return nativeHTTP(nativeSSE(strings.Repeat("old answer ", 1200), "stop")), nil
		case 2:
			return nativeHTTP(nativeSSE("", "tool_calls", nativeCall("write-once", "write_file", `{"path":"result.txt","content":"done"}`))), nil
		case 3:
			response := nativeHTTP(`{"error":"context_length_exceeded"}`)
			response.StatusCode = 400
			return response, nil
		case 4:
			if err := validateNativeMessages(request.Messages); err != nil {
				t.Fatal(err)
			}
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" || last.ToolCallID != "write-once" {
				t.Fatal("retry lost durable tool result")
			}
			for _, m := range request.Messages {
				if strings.Count(m.Content, "old answer") > 20 {
					t.Fatal("retried unchanged oversized history")
				}
			}
			return nativeHTTP(nativeSSE("finished", "stop")), nil
		default:
			t.Fatal("unbounded retries")
			return nil, nil
		}
	})}}
	ctx := withNativeExecution(context.Background(), run)
	first, err := a.SingleShot(ctx, SingleShotOpts{Cwd: root, Message: "first task", Tools: "edits"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.ResumeStream(ctx, first.SessionID, ResumeOpts{Cwd: root, Message: "write result", Tools: "edits"}, func(e StreamEvent) {
		if e.Type == "tool_start" {
			executed++
		}
	})
	if err != nil || requests != 4 || executed != 1 {
		t.Fatalf("requests=%d executed=%d err=%v", requests, executed, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "result.txt")); err != nil || string(data) != "done" {
		t.Fatalf("write lost: %q %v", data, err)
	}
}
