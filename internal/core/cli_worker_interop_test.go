package core

import (
	"context"
	"strings"
	"testing"
)

func TestClaudeWorkerResultFailuresKeepPartialActivity(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"result","is_error":true,"result":"Provider rejected the request"}`,
		`{"type":"result","subtype":"error_during_execution","errors":["Provider rejected the request"]}`,
		`{"type":"error","error":{"message":"Provider rejected the request"}}`,
	} {
		t.Run(terminal, func(t *testing.T) {
			body := `{"type":"assistant","session_id":"worker-session","message":{"id":"m","content":[{"type":"text","text":"partial answer"}],"usage":{"input_tokens":12,"output_tokens":3}}}` + "\n" + terminal
			emit, events := collectEvents()
			reply, err := parseClaudeStream(strings.NewReader(body), emit)
			if err == nil || !strings.Contains(err.Error(), "Provider rejected") || reply.Text != "partial answer" || reply.SessionID != "worker-session" {
				t.Fatalf("failed turn lost its error or partial output: %+v %v", reply, err)
			}
			var reportedError, reportedUsage bool
			for _, e := range *events {
				reportedError = reportedError || e.Type == "error" && strings.Contains(e.Detail, "Provider rejected")
				reportedUsage = reportedUsage || e.Type == "usage" && e.Usage != nil && e.Usage.PromptTokens == 12
			}
			if !reportedError || !reportedUsage {
				t.Fatalf("worker monitor lost error/usage: %+v", *events)
			}
		})
	}
}

func TestClaudeWorkerLifecycleAndReportedReasoning(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"session","model":"chosen-model"}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"Checking dependencies."}}}`,
		`{"type":"assistant","message":{"id":"m1","content":[{"type":"thinking","thinking":"Checking dependencies."},{"type":"text","text":"answer"}]}}`,
		`{"type":"stream_event","event":{"type":"message_stop"}}`,
		`{"type":"stream_event","event":{"type":"message_start"}}`,
		`{"type":"assistant","message":{"id":"m2","content":[{"type":"thinking","thinking":"Reported fallback."},{"type":"redacted_thinking","data":"opaque-private-value"}]}}`,
		`{"type":"result","result":"answer"}`,
	}, "\n")
	emit, events := collectEvents()
	reply, err := parseClaudeStream(strings.NewReader(body), emit)
	if err != nil || reply.Text != "answer" {
		t.Fatal(reply, err)
	}
	var reasoning strings.Builder
	var session, start, finish, model bool
	for _, e := range *events {
		if e.Type == "reasoning" {
			reasoning.WriteString(e.Text)
		}
		session = session || e.Type == "status" && e.ID == "session"
		start = start || e.Type == "step_start"
		finish = finish || e.Type == "step_finish"
		model = model || e.Type == "model" && e.Model == "chosen-model"
	}
	if !session || !start || !finish || !model || reasoning.String() != "Checking dependencies.Reported fallback." {
		t.Fatalf("missing lifecycle or duplicated/private reasoning: %+v", *events)
	}
}

func TestClaudeWorkerTruncatedProcessCannotSucceed(t *testing.T) {
	skipOnWindows(t)
	for _, cli := range []string{"claude", "openclaude"} {
		t.Run(cli, func(t *testing.T) {
			fakeBinOnPath(t, cli, `cat >/dev/null
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}'`)
			a := NewClaudeAdapter()
			if cli == "openclaude" {
				a = NewOpenClaudeAdapter()
			}
			reply, err := a.SingleShotStream(context.Background(), SingleShotOpts{Cwd: t.TempDir(), Message: "Plan"}, nil)
			if err == nil || !strings.Contains(err.Error(), cli+" stream:") || reply == nil || reply.Text != "partial" {
				t.Fatalf("truncated process incorrectly succeeded: %+v %v", reply, err)
			}
		})
	}
}

func TestOpenCodeWorkerSessionFailurePreservesReference(t *testing.T) {
	body := `{"type":"text","sessionID":"s","part":{"text":"partial"}}` + "\n" + `{"type":"session.error","properties":{"sessionID":"s","error":{"name":"UnknownError","data":{"message":"Unexpected server error. Check server logs for details.","ref":"err_fixture","headers":{"Authorization":"fixture-secret"},"stack":"fixture-private-stack"}}}}`
	emit, events := collectEvents()
	reply, err := parseOpenCodeStream(strings.NewReader(body), emit)
	if err == nil || !strings.Contains(err.Error(), "err_fixture") || strings.Contains(err.Error(), "fixture-secret") || strings.Contains(err.Error(), "fixture-private-stack") || reply.Text != "partial" {
		t.Fatalf("session.error lost failure/reference or leaked internals: %+v %v", reply, err)
	}
	var startup, failure bool
	for _, e := range *events {
		startup = startup || e.Type == "status" && e.ID == "s"
		failure = failure || e.Type == "error" && strings.Contains(e.Detail, "err_fixture")
	}
	if !startup || !failure {
		t.Fatalf("missing live status/error: %+v", *events)
	}
}

func TestCopilotWorkerReasoningAndLifecycle(t *testing.T) {
	body := strings.Join([]string{
		`{"type":"session.start","data":{"sessionId":"s","selectedModel":"chosen-model"}}`,
		`{"type":"assistant.turn_start","data":{"turnId":"t"}}`,
		`{"type":"assistant.reasoning_delta","data":{"reasoningId":"r","deltaContent":"Checking scope."}}`,
		`{"type":"assistant.reasoning","data":{"reasoningId":"r","content":"Checking scope."}}`,
		`{"type":"assistant.reasoning","data":{"reasoningId":"r2","content":"Reported fallback."}}`,
		`{"type":"assistant.reasoning_delta","agentId":"child","data":{"reasoningId":"r3","deltaContent":"Child activity."}}`,
		`{"type":"assistant.message","data":{"messageId":"m","content":"answer"}}`,
		`{"type":"assistant.turn_end","data":{"turnId":"t"}}`,
		`{"type":"session.idle","data":{}}`,
	}, "\n")
	emit, events := collectEvents()
	reply, err := parseAdditionalCLIStream(strings.NewReader(body), "copilot", emit)
	if err != nil || reply.Text != "answer" {
		t.Fatal(reply, err)
	}
	var reasoning strings.Builder
	var session, start, finish bool
	for _, e := range *events {
		if e.Type == "reasoning" {
			reasoning.WriteString(e.Text)
		}
		session = session || e.Type == "status" && e.ID == "s"
		start = start || e.Type == "step_start"
		finish = finish || e.Type == "step_finish"
	}
	if !session || !start || !finish || reasoning.String() != "Checking scope.Reported fallback.Child activity." {
		t.Fatalf("missing lifecycle or duplicated reasoning: %+v", *events)
	}
}

func TestAdditionalWorkerFailuresReachLiveMonitor(t *testing.T) {
	for _, tc := range []struct{ cli, terminal, want string }{
		{"copilot", `{"type":"session.error","data":{"message":"Provider unavailable"}}` + "\n" + `{"type":"result","exitCode":0}`, "Provider unavailable"},
		{"copilot", `{"type":"result","exitCode":1,"outcome":"blocked"}`, "blocked"},
		{"copilot", `{"type":"session.idle","data":{"aborted":true}}`, "aborted"},
		{"antigravity", `{"event":"result","result":{"status":"ERROR","error":"Provider unavailable"}}`, "Provider unavailable"},
	} {
		t.Run(tc.cli+tc.want, func(t *testing.T) {
			emit, events := collectEvents()
			_, err := parseAdditionalCLIStream(strings.NewReader(tc.terminal), tc.cli, emit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("terminal failure reported as success: %v", err)
			}
			var failure bool
			for _, e := range *events {
				failure = failure || e.Type == "error" && strings.Contains(e.Detail, tc.want)
			}
			if !failure {
				t.Fatalf("monitor did not receive the failure: %+v", *events)
			}
		})
	}
}

func TestWorkerStructuredStreamCannotFinishDuringActiveTurn(t *testing.T) {
	codex := parseCodexStream(strings.NewReader(`{"type":"turn.started"}`+"\n"+`{"type":"item.completed","item":{"type":"agent_message","text":"partial"}}`), nil)
	if codex.Err == nil || codex.Text != "partial" {
		t.Fatalf("active Codex turn incorrectly succeeded: %+v", codex)
	}
	for _, start := range []string{`{"type":"step_start"}`, `{"type":"message.part.updated","properties":{"part":{"type":"step-start"}}}`} {
		reply, err := parseOpenCodeStream(strings.NewReader(start+"\n"+`{"type":"text","part":{"text":"partial"}}`), nil)
		if err == nil || reply.Text != "partial" {
			t.Fatalf("active OpenCode step incorrectly succeeded: %+v %v", reply, err)
		}
	}
}
