package core

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestNativeUnknownWindowDoesNotRejectLargeCurrentRequest(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextSource = "fallback; server window unknown"
	run.local.OutputAutomatic = true
	task := strings.Repeat("requirement ", 2500)
	calls := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if _, exists := request["max_tokens"]; exists {
			t.Fatal("imposed a generation ceiling for an unknown backend window")
		}
		var messages []nativeMessage
		_ = json.Unmarshal(request["messages"], &messages)
		if messages[len(messages)-1].Content != task {
			t.Fatal("truncated the current task")
		}
		return nativeHTTP(nativeSSE("done", "stop")), nil
	})}}
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: task})
	if err != nil || reply.Text != "done" || calls != 1 {
		t.Fatalf("reply=%+v err=%v calls=%d", reply, err, calls)
	}
}

func TestNativeBackendRejectionLearnsPhysicalWindowAndCompacts(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextSource = "fallback; server window unknown"
	run.local.OutputAutomatic = true
	calls := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Messages  []nativeMessage `json:"messages"`
			MaxTokens int             `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch calls {
		case 1:
			return nativeHTTP(nativeSSE(strings.Repeat("old answer ", 1000), "stop")), nil
		case 2:
			response := nativeHTTP(`{"error":{"message":"This model's maximum context length is 4096 tokens. However, you requested 6100 tokens.","n_ctx":4096}}`)
			response.StatusCode = 400
			return response, nil
		case 3:
			if request.MaxTokens < 1 || request.MaxTokens >= 4096 || !strings.HasPrefix(request.Messages[len(request.Messages)-1].Content, "keep task") {
				t.Fatal("lost task or backend limit", request.MaxTokens)
			}
			for _, m := range request.Messages {
				if strings.Count(m.Content, "old answer") > 20 {
					t.Fatal("did not compact rejected history")
				}
			}
			return nativeHTTP(nativeSSE("done", "stop")), nil
		default:
			t.Fatal("unexpected replay")
		}
		return nil, nil
	})}}
	ctx := withNativeExecution(context.Background(), run)
	first, err := a.SingleShot(ctx, SingleShotOpts{Cwd: t.TempDir(), Message: "first"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := c.GetSetting(context.Background(), ScopeCLI, "native.session."+first.SessionID)
	var session nativeSession
	_ = json.Unmarshal(raw, &session)
	reply, err := a.Resume(ctx, first.SessionID, ResumeOpts{Cwd: session.Cwd, Message: "keep task"})
	if err != nil || reply.Text != "done" || calls != 3 {
		t.Fatalf("reply=%+v err=%v calls=%d", reply, err, calls)
	}
	window, source := c.nativeLimits.lookup(context.Background(), run.local)
	if window != 4096 || source != "backend context rejection" {
		t.Fatal(window, source)
	}
}

func TestNativeRejectedWindowUsesOnlyExplicitContextLimits(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    int
	}{
		{`This model's maximum context length is 32768 tokens. However, you requested 35000 tokens.`, 32768},
		{`{"error":{"type":"exceed_context_size_error","n_ctx":8192,"n_prompt_tokens":10000}}`, 8192},
		{`model loaded with context length of only 4096 tokens`, 4096},
		{`requested 12000 tokens, max_tokens 1024`, 0},
		{`{"error":{"n_ctx":-1}}`, 0},
	} {
		if got := nativeRejectedWindow(tc.message); got != tc.want {
			t.Fatalf("%s: got %d want %d", tc.message, got, tc.want)
		}
	}
}

func TestNativeUnknownOutputAutomaticallyRaisesBackendDefaultAfterLength(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextSource = "fallback; server window unknown"
	run.local.OutputAutomatic = true
	calls := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&request)
		if calls == 1 {
			if request["max_tokens"] != nil {
				t.Fatal("first automatic request must use the backend default")
			}
			return nativeHTTP(`data: {"choices":[{"delta":{"reasoning_content":"unfinished planning"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":1024,"total_tokens":1124}}` + "\n\ndata: [DONE]\n\n"), nil
		}
		var limit int
		_ = json.Unmarshal(request["max_tokens"], &limit)
		if limit <= 1024 {
			t.Fatal("did not adapt beyond the exhausted backend default", limit)
		}
		return nativeHTTP(nativeSSE("answer", "stop")), nil
	})}}
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: "reason and answer"})
	if err != nil || reply.Text != "answer" || calls != 2 {
		t.Fatalf("reply=%+v err=%v calls=%d", reply, err, calls)
	}
}

func TestNativeAutomaticReasoningCanGrowPastEightThousandTokens(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.ContextSource = "fallback; server window unknown"
	run.local.OutputAutomatic = true
	calls := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			MaxTokens int `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.MaxTokens >= 16384 {
			return nativeHTTP(nativeSSE("finished reasoning", "stop")), nil
		}
		limit := max(1024, request.MaxTokens)
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"reasoning_content": "still reasoning"}, "finish_reason": "length"}}, "usage": NativeUsage{100, limit, 100 + limit}})
		return nativeHTTP("data: " + string(chunk) + "\n\ndata: [DONE]\n\n"), nil
	})}}
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: "reason then finish"})
	if err != nil || reply.Text != "finished reasoning" || calls != 5 {
		t.Fatalf("reply=%+v err=%v calls=%d", reply, err, calls)
	}
}

func TestNativeAutomaticReserveYieldsToCurrentRequest(t *testing.T) {
	c := nativeTestCore(t)
	run := nativeTestRun(c)
	run.local.OutputAutomatic = true
	run.modelOnly = true
	task := strings.Repeat("fact ", 2400)
	calls := 0
	a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		var request struct {
			Messages  []nativeMessage `json:"messages"`
			MaxTokens int             `json:"max_tokens"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Messages[len(request.Messages)-1].Content != task || request.MaxTokens <= 0 || request.MaxTokens >= run.local.OutputTokens {
			t.Fatalf("request did not adapt its output reserve: %d", request.MaxTokens)
		}
		return nativeHTTP(nativeSSE("done", "stop")), nil
	})}}
	reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: task})
	if err != nil || reply.Text != "done" || calls != 1 {
		t.Fatalf("reply=%+v err=%v calls=%d", reply, err, calls)
	}
}

func TestNativeCompactionDoesNotRequireAnUnaffordableReceipt(t *testing.T) {
	messages := []nativeMessage{{Role: "system", Content: "mandatory"}, {Role: "user", Content: "old task"}, {Role: "assistant", Content: strings.Repeat("history ", 2000)}, {Role: "user", Content: "keep every requirement"}}
	budget := nativeMessageTokens([]nativeMessage{messages[0], messages[3]}) + 10
	compacted, changed, err := compactNativeContext(messages, budget, nativeMessageTokens)
	if err != nil || !changed || nativeMessageTokens(compacted) > budget || !strings.HasPrefix(compacted[len(compacted)-1].Content, "keep every requirement") {
		t.Fatalf("compaction=%+v changed=%v err=%v", compacted, changed, err)
	}
}

func TestNativeBackendDecidesWhenMandatoryInputEstimateExceedsItsWindow(t *testing.T) {
	for _, reject := range []bool{false, true} {
		c := nativeTestCore(t)
		run := nativeTestRun(c)
		run.local.ContextSource = "OpenAI model serving metadata"
		run.local.OutputAutomatic = true
		task := strings.Repeat("requirement ", 2500)
		calls := 0
		a := &nativeCLIAdapter{http: &http.Client{Transport: nativeTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			var request map[string]json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["max_tokens"] != nil {
				t.Fatal("guessed output allowance from an uncertain oversized estimate")
			}
			if reject {
				res := nativeHTTP(`{"error":"maximum context length is 8192 tokens"}`)
				res.StatusCode = 400
				return res, nil
			}
			return nativeHTTP(nativeUsageSSE("fits the backend tokenizer", 6000, 10)), nil
		})}}
		reply, err := a.SingleShot(withNativeExecution(context.Background(), run), SingleShotOpts{Cwd: t.TempDir(), Message: task})
		if calls != 1 || (reject && (err == nil || !strings.Contains(err.Error(), "native model HTTP"))) || (!reject && (err != nil || reply.Text != "fits the backend tokenizer")) {
			t.Fatalf("reject=%v reply=%+v err=%v calls=%d", reject, reply, err, calls)
		}
	}
}
