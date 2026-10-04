package core

import (
	"strings"
	"testing"
)

func collectEvents() (StreamHandler, *[]StreamEvent) {
	var events []StreamEvent
	return func(ev StreamEvent) { events = append(events, ev) }, &events
}

// Pins the claude stream-json translation: text deltas stream, tool_use
// becomes tool_start with a human detail, tool_result closes it, and
// the result line yields the final Reply with session id.
func TestParseClaudeStream_DeltasToolsAndResult(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"system","subtype":"init","session_id":"sess-1"}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}}`,
		`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls -la"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_1","is_error":false}]}}`,
		`{"type":"result","result":"Hello — done.","is_error":false,"session_id":"sess-1"}`,
	}, "\n") + "\n"

	emit, events := collectEvents()
	reply, err := parseClaudeStream(strings.NewReader(stream), emit)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if reply.Text != "Hello — done." || reply.SessionID != "sess-1" {
		t.Fatalf("reply = %+v", reply)
	}

	var text string
	var starts, ends int
	for _, ev := range *events {
		switch ev.Type {
		case "text":
			text += ev.Text
		case "tool_start":
			starts++
			if ev.Tool != "Bash" || ev.Detail != "ls -la" || ev.ID != "tu_1" {
				t.Errorf("tool_start = %+v", ev)
			}
		case "tool_end":
			ends++
			if !ev.OK || ev.ID != "tu_1" {
				t.Errorf("tool_end = %+v", ev)
			}
		}
	}
	// Deltas only — the complete assistant text block must NOT double
	// the streamed text.
	if text != "Hello" {
		t.Errorf("streamed text = %q (assistant block double-counted?)", text)
	}
	if starts != 1 || ends != 1 {
		t.Errorf("tool events = %d/%d", starts, ends)
	}
}

// Older claude versions don't emit partial deltas — complete assistant
// text blocks must stream instead.
func TestParseClaudeStream_FallsBackToAssistantBlocks(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"whole message"}]}}`,
		`{"type":"result","result":"whole message","session_id":"s"}`,
	}, "\n") + "\n"
	emit, events := collectEvents()
	if _, err := parseClaudeStream(strings.NewReader(stream), emit); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var text string
	for _, ev := range *events {
		if ev.Type == "text" {
			text += ev.Text
		}
	}
	if text != "whole message" {
		t.Errorf("streamed text = %q", text)
	}
}

// A stream that dies before the result line (crash / kill) returns the
// accumulated text plus an error, so interrupts keep partial output.
func TestParseClaudeStream_PartialOnTruncatedStream(t *testing.T) {
	stream := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial answ"}}}` + "\n"
	reply, err := parseClaudeStream(strings.NewReader(stream), nil)
	if err == nil {
		t.Fatal("want error for stream without result event")
	}
	if reply == nil || reply.Text != "partial answ" {
		t.Fatalf("partial reply = %+v", reply)
	}
}

// Pins the codex proto-style schema translation.
func TestParseCodexStream_ProtoSchema(t *testing.T) {
	stream := strings.Join([]string{
		`{"id":"0","msg":{"type":"session_configured","session_id":"x"}}`,
		`{"id":"1","msg":{"type":"exec_command_begin","command":["bash","-lc","go test ./..."]}}`,
		`{"id":"1","msg":{"type":"exec_command_end","exit_code":0}}`,
		`{"id":"2","msg":{"type":"agent_message_delta","delta":"All "}}`,
		`{"id":"2","msg":{"type":"agent_message_delta","delta":"green."}}`,
		`{"id":"2","msg":{"type":"agent_message","message":"All green."}}`,
		`{"id":"3","msg":{"type":"task_complete","last_agent_message":"All green."}}`,
	}, "\n") + "\n"

	emit, events := collectEvents()
	result := parseCodexStream(strings.NewReader(stream), emit)
	if result.Text != "All green." {
		t.Fatalf("accumulated text = %q", result.Text)
	}
	var sawStart, sawEnd bool
	var streamed string
	for _, ev := range *events {
		switch ev.Type {
		case "tool_start":
			sawStart = true
			if ev.Tool != "shell" || ev.Detail != "bash -lc go test ./..." {
				t.Errorf("tool_start = %+v", ev)
			}
		case "tool_end":
			sawEnd = true
			if !ev.OK {
				t.Errorf("tool_end not ok: %+v", ev)
			}
		case "text":
			streamed += ev.Text
		}
	}
	if !sawStart || !sawEnd {
		t.Error("missing shell tool events")
	}
	// Deltas streamed; the complete agent_message must not double them.
	if streamed != "All green." {
		t.Errorf("streamed = %q", streamed)
	}
}

// Pins the codex thread-style (item.*) schema translation.
func TestParseCodexStream_ItemSchema(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.started","item":{"id":"item_0","item_type":"command_execution","command":"ls -la","status":"in_progress"}}`,
		`{"type":"item.completed","item":{"id":"item_0","item_type":"command_execution","command":"ls -la","status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"item_1","item_type":"agent_message","text":"There are 3 files."}}`,
		`{"type":"turn.completed","usage":{}}`,
	}, "\n") + "\n"

	emit, events := collectEvents()
	result := parseCodexStream(strings.NewReader(stream), emit)
	if result.Text != "There are 3 files." {
		t.Fatalf("accumulated text = %q", result.Text)
	}
	var sawStart, sawEnd bool
	for _, ev := range *events {
		if ev.Type == "tool_start" && ev.Tool == "shell" && ev.Detail == "ls -la" && ev.ID == "item_0" {
			sawStart = true
		}
		if ev.Type == "tool_end" && ev.ID == "item_0" && ev.OK {
			sawEnd = true
		}
	}
	if !sawStart || !sawEnd {
		t.Errorf("missing item tool events; got %+v", *events)
	}
}

func TestParseCodexStream_ReportsStartupReasoningAndBackendFailures(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"worker-session"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"reasoning-1","type":"reasoning","text":"Preparing bounded implementation tasks."}}`,
		`{"type":"error","message":"Reconnecting after a transport error"}`,
		`{"type":"turn.failed","error":{"message":"Selected model is unavailable"}}`,
	}, "\n") + "\n"
	emit, events := collectEvents()
	result := parseCodexStream(strings.NewReader(stream), emit)
	if result.SessionID != "worker-session" || result.Text != "" {
		t.Fatalf("reasoning leaked into the final plan: %+v", result)
	}
	var startup, reasoning, failure bool
	for _, event := range *events {
		startup = startup || event.Type == "status"
		reasoning = reasoning || event.Type == "reasoning" && event.Text == "Preparing bounded implementation tasks."
		failure = failure || event.Type == "error" && event.Detail == "Selected model is unavailable"
	}
	if !startup || !reasoning || !failure {
		t.Fatalf("Codex activity was silently discarded: %+v", *events)
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "Selected model is unavailable") {
		t.Fatalf("failed turn was treated as success: %+v", result)
	}
}

func TestParseCodexStreamRecoverableErrorsDoNotFailSuccessfulTurn(t *testing.T) {
	result := parseCodexStream(strings.NewReader("{\"type\":\"error\",\"message\":\"Reconnecting\"}\n{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}\n"), nil)
	if result.Err != nil {
		t.Fatalf("recoverable reconnect became a failure: %v", result.Err)
	}
}

// Unknown event lines and non-JSON noise must be ignored, not fatal — a
// codex/claude upgrade degrades to fewer live events, never a broken turn.
func TestParseStreams_TolerateUnknownLines(t *testing.T) {
	claude := "not json at all\n" +
		`{"type":"mystery_event","payload":123}` + "\n" +
		`{"type":"result","result":"ok","session_id":"s"}` + "\n"
	reply, err := parseClaudeStream(strings.NewReader(claude), nil)
	if err != nil || reply.Text != "ok" {
		t.Fatalf("claude: reply=%+v err=%v", reply, err)
	}

	codex := "WARN some log line\n" +
		`{"type":"future.event","item":{"item_type":"hologram"}}` + "\n" +
		`{"id":"1","msg":{"type":"agent_message","message":"fine"}}` + "\n"
	if result := parseCodexStream(strings.NewReader(codex), nil); result.Text != "fine" {
		t.Fatalf("codex: text=%q", result.Text)
	}
}

func TestSummarizeToolInput(t *testing.T) {
	if got := summarizeToolInput(map[string]any{"command": "go build ./..."}); got != "go build ./..." {
		t.Errorf("command: %q", got)
	}
	if got := summarizeToolInput(map[string]any{"file_path": "/tmp/x.go", "content": "..."}); got != "/tmp/x.go" {
		t.Errorf("file_path: %q", got)
	}
	if got := summarizeToolInput(nil); got != "" {
		t.Errorf("empty: %q", got)
	}
}
