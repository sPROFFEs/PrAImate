package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAdditionalCLIStreams(t *testing.T) {
	for _, tc := range []struct {
		cli, body, text, session string
		input, output            int64
	}{
		{"copilot", `{"type":"session.start","data":{"sessionId":"cp-1","selectedModel":"test-model"}}
{"type":"assistant.message_delta","data":{"messageId":"msg","deltaContent":"Hello"}}
{"type":"assistant.message","data":{"messageId":"msg","content":"Hello"}}
{"type":"assistant.usage","id":"usage-1","data":{"model":"test-model","inputTokens":100,"outputTokens":20}}
{"type":"assistant.usage","id":"usage-1","data":{"model":"test-model","inputTokens":100,"outputTokens":20}}
{"type":"result","sessionId":"cp-1","exitCode":0,"usage":{"premiumRequests":1}}`, "Hello", "cp-1", 100, 20},
		{"antigravity", `{"event":"init","conversation_id":"ag-1","init":{"model":"test-model"}}
{"event":"step_update","step_update":{"step_index":9,"state":"ACTIVE","step_type":"agent_response","text_delta":"Hello"}}
{"event":"step_update","step_update":{"step_index":9,"state":"DONE","step_type":"agent_response","usage":{"input_tokens":100,"output_tokens":20}}}
{"event":"result","result":{"conversation_id":"ag-1","status":"SUCCESS","response":"Hello","usage":{"input_tokens":9000,"output_tokens":400}}}`, "Hello", "ag-1", 100, 20},
	} {
		t.Run(tc.cli, func(t *testing.T) {
			var usage UsageAccumulator
			var text strings.Builder
			reply, err := parseAdditionalCLIStream(strings.NewReader(tc.body), tc.cli, func(e StreamEvent) {
				usage.Observe(e)
				if e.Type == "text" {
					text.WriteString(e.Text)
				}
			})
			if err != nil || reply.Text != tc.text || reply.SessionID != tc.session || text.String() != tc.text {
				t.Fatalf("reply=%+v text=%q err=%v", reply, text.String(), err)
			}
			in, out, _, model := usage.Snapshot()
			if in != tc.input || out != tc.output || model != "test-model" {
				t.Fatalf("usage=%d/%d model=%s", in, out, model)
			}
		})
	}
}

// Opt-in protocol check against a downloaded official binary. All inference
// goes to a disposable local fixture, with a separate CLI profile and no login.
func TestCopilotOfficialBinaryIntegration(t *testing.T) {
	binary := os.Getenv("PRAIMATE_TEST_COPILOT_BINARY")
	if binary == "" {
		t.Skip("set PRAIMATE_TEST_COPILOT_BINARY for the official CLI protocol check")
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "invalid fixture request", 400)
			return
		}
		usage := map[string]int{"prompt_tokens": 100, "completion_tokens": 5, "total_tokens": 105}
		if !request.Stream {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "model": "test-model", "choices": []any{map[string]any{"index": 0, "message": map[string]string{"role": "assistant", "content": "fixture answer"}, "finish_reason": "stop"}}, "usage": usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []string{
			`{"id":"fixture","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"fixture answer"},"finish_reason":null}]}`,
			`{"id":"fixture","object":"chat.completion.chunk","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()
	env := map[string]string{"COPILOT_HOME": t.TempDir(), "COPILOT_AUTO_UPDATE": "false", "DO_NOT_TRACK": "1", "COPILOT_PROVIDER_BASE_URL": provider.URL + "/v1", "COPILOT_PROVIDER_TYPE": "openai", "COPILOT_PROVIDER_WIRE_API": "completions", "COPILOT_PROVIDER_API_KEY": "", "COPILOT_PROVIDER_API_KEY_COMMAND": "", "COPILOT_PROVIDER_BEARER_TOKEN": "", "COPILOT_GITHUB_TOKEN": "", "GH_TOKEN": "", "GITHUB_TOKEN": ""}
	a := NewCopilotAdapter()
	a.bin, a.extraDirs = binary, nil
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dir, session := t.TempDir(), ""
	for turn := 0; turn < 2; turn++ {
		var usage UsageAccumulator
		var reply *Reply
		var err error
		if turn == 0 {
			reply, err = a.SingleShotStream(ctx, SingleShotOpts{Cwd: dir, Model: "test-model", Message: "Say fixture answer.", Env: env}, usage.Observe)
		} else {
			reply, err = a.ResumeStream(ctx, session, ResumeOpts{Cwd: dir, Model: "test-model", Message: "Say it again.", Env: env}, usage.Observe)
		}
		if err != nil {
			t.Fatal(err)
		}
		if reply.SessionID == "" || (session != "" && reply.SessionID != session) || !strings.Contains(reply.Text, "fixture answer") {
			t.Fatalf("unexpected reply: %+v", reply)
		}
		session = reply.SessionID
		input, output, calls, model := usage.Snapshot()
		if input != 100 || output != 5 || calls != 1 || model != "test-model" {
			t.Fatalf("turn %d usage=%d/%d calls=%d model=%s", turn, input, output, calls, model)
		}
	}
}

func TestCopilotResultFailure(t *testing.T) {
	reply, err := parseAdditionalCLIStream(strings.NewReader(`{"type":"result","sessionId":"session","exitCode":1,"outcome":"blocked"}`), "copilot", nil)
	if err == nil || reply.SessionID != "session" || reply.ExitCode != 1 {
		t.Fatalf("reply=%+v err=%v", reply, err)
	}
}

func TestAdditionalCLIAdapterLaunchAndResume(t *testing.T) {
	skipOnWindows(t)
	for _, cli := range []string{"copilot", "antigravity"} {
		t.Run(cli, func(t *testing.T) {
			dir := t.TempDir()
			binary := cli
			if cli == "antigravity" {
				binary = "agy"
			}
			body := `{"type":"session.start","data":{"sessionId":"new-id"}}` + "\n" + `{"type":"assistant.message","data":{"content":"done"}}` + "\n" + `{"type":"session.idle","data":{}}`
			if cli == "antigravity" {
				body = `{"event":"result","result":{"status":"SUCCESS","conversation_id":"new-id","response":"done"}}`
			}
			fakeBinOnPath(t, binary, `cat > stdin.txt
printf '%s\n' "$@" > argv.txt
printf '%s\n' '`+body+`'`)
			a := NewCopilotAdapter()
			if cli == "antigravity" {
				a = NewAntigravityAdapter()
				a.extraDirs = nil
			}
			message := "first line\nsecond line"
			reply, err := a.SingleShot(context.Background(), SingleShotOpts{Cwd: dir, Message: message, SystemPrompt: "persona", Model: "chosen-model", Tools: "full"})
			if err != nil || reply.Text != "done" || reply.SessionID != "new-id" {
				t.Fatalf("reply=%+v err=%v", reply, err)
			}
			stdin, _ := os.ReadFile(filepath.Join(dir, "stdin.txt"))
			args, _ := os.ReadFile(filepath.Join(dir, "argv.txt"))
			if !strings.Contains(string(stdin), "second line") || !strings.Contains(string(stdin), "persona") || strings.Contains(string(args), "second line") || !strings.Contains(string(args), "chosen-model") {
				t.Fatalf("stdin=%s args=%s", stdin, args)
			}
			_, err = a.Resume(context.Background(), "new-id", ResumeOpts{Cwd: dir, Message: message, Model: "other-model"})
			if err != nil {
				t.Fatal(err)
			}
			args, _ = os.ReadFile(filepath.Join(dir, "argv.txt"))
			if !strings.Contains(string(args), "new-id") || !strings.Contains(string(args), "other-model") || strings.Contains(string(args), "--allow-all") || strings.Contains(string(args), "--dangerously-skip-permissions") {
				t.Fatalf("resume policy/model lost: %s", args)
			}
		})
	}
}

func TestNewCLIManagedPermissions(t *testing.T) {
	args, err := additionalCLIArgs("copilot", "test", "", "session")
	if err != nil || !strings.Contains(strings.Join(args, " "), "--available-tools=view,glob,grep") {
		t.Fatal(args, err)
	}
	if !supportsManagedSafeMode(NewCopilotAdapter()) || supportsManagedSafeMode(NewAntigravityAdapter()) {
		t.Fatal("incorrect managed safety claim")
	}
}

func TestAdditionalCLIStreamPreservesPartialFailure(t *testing.T) {
	for _, cli := range []string{"copilot", "antigravity"} {
		body := `{"type":"assistant.message","data":{"content":"partial"}}` + "\n" + `{"type":"session.error","data":{"message":"failed"}}`
		if cli == "antigravity" {
			body = `{"event":"step_update","step_update":{"step_type":"agent_response","text_delta":"partial"}}` + "\n" + `{"event":"result","result":{"status":"ERROR","error":"failed"}}`
		}
		reply, err := parseAdditionalCLIStream(strings.NewReader(body), cli, nil)
		if err == nil || reply.Text != "partial" {
			t.Fatalf("%s reply=%+v err=%v", cli, reply, err)
		}
	}
}
