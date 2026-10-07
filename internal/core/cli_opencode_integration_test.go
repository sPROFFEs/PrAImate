package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt in with an absolute path to an independently downloaded OpenCode binary.
// No real model/account is used: all completion requests go to the fixture.
func TestOpenCodeInstalledCompatibility(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_OPENCODE_BINARY")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_OPENCODE_BINARY to test an installed OpenCode version")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("PRAIMATE_TEST_OPENCODE_BINARY must be absolute")
	}
	if runtime.GOOS == "windows" {
		t.Skip("command fixture currently uses a POSIX shell")
	}
	root := t.TempDir()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(root, "data", "opencode", "log", "*.log"))
		for _, path := range logs {
			body, _ := os.ReadFile(path)
			for _, line := range strings.Split(string(body), "\n") {
				if strings.Contains(strings.ToLower(line), "error") {
					t.Logf("OpenCode fixture log: %s", truncate(line, 4000))
				}
			}
		}
	})
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0700); err != nil {
		t.Fatal(err)
	}
	// Desktop/Studio may start elsewhere. The selected project must win over
	// the parent's PWD; OpenCode explicitly uses PWD when choosing its root.
	t.Setenv("PWD", root)
	skillDir := filepath.Join(project, ".opencode", "skills", "compat-skill")
	if err := os.MkdirAll(skillDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: compat-skill\ndescription: Isolated compatibility fixture\n---\nReturn compat-skill-ok when loaded.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	isolateOpenCodeFixture(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, bin, "--version")
	version.Dir = project
	if out, err := version.CombinedOutput(); err != nil {
		t.Fatalf("OpenCode version: %v: %s", err, out)
	} else {
		t.Logf("OpenCode %s", strings.TrimSpace(string(out)))
	}
	var mcpRequested, commandRequested, skillRequested atomic.Bool
	var toolsLogged atomic.Bool
	var quotaExceeded atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if quotaExceeded.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{"error":{"message":"Free usage exceeded, subscribe to Go","type":"FreeUsageLimitError"}}`)
			return
		}
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Tools) > 0 && toolsLogged.CompareAndSwap(false, true) {
			var names []string
			for _, tool := range request.Tools {
				names = append(names, tool.Function.Name)
			}
			t.Logf("OpenCode fixture tools: %v", names)
		}
		latestUser := ""
		for _, msg := range request.Messages {
			if msg.Role == "user" {
				body, _ := json.Marshal(msg.Content)
				latestUser = string(body)
			}
		}
		delta := map[string]any{"content": "Compatibility fixture"}
		finish := "stop"
		toolName, arguments := "", ""
		if strings.Contains(latestUser, "compat-start") {
			delta["content"] = "compat-start-ok"
			if !skillRequested.Load() {
				for _, tool := range request.Tools {
					if tool.Function.Name == "skill" {
						toolName, arguments = "skill", `{"name":"compat-skill"}`
						skillRequested.Store(true)
						break
					}
				}
			}
			if toolName == "" && !mcpRequested.Load() {
				for _, tool := range request.Tools {
					if strings.HasSuffix(tool.Function.Name, "echo") {
						toolName, arguments = tool.Function.Name, `{"value":"mcp-ok"}`
						mcpRequested.Store(true)
						break
					}
				}
			}
		} else if strings.Contains(latestUser, "compat-resume") {
			delta["content"] = "compat-resume-ok"
			if !commandRequested.Load() {
				toolName, arguments = "bash", `{"command":"printf compatibility-ok > compat.txt","description":"Write isolated compatibility fixture"}`
				commandRequested.Store(true)
			}
		}
		if toolName != "" {
			finish = "tool_calls"
			delta = map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": "fixture-call", "type": "function",
				"function": map[string]any{"name": toolName, "arguments": arguments},
			}}}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta any, reason any, usage any) {
			body, _ := json.Marshal(map[string]any{
				"id": "fixture", "object": "chat.completion.chunk", "created": 1, "model": "compat",
				"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": reason}}, "usage": usage,
			})
			fmt.Fprintf(w, "data: %s\n\n", body)
		}
		chunk(delta, nil, nil)
		chunk(map[string]any{}, finish, map[string]any{"prompt_tokens": 40, "completion_tokens": 8, "total_tokens": 48})
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer backend.Close()
	if err := writeOpenCodeLocalRoute(project, ChatLocalEndpoint{Endpoint: backend.URL, Model: "compat"}); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"OPENAI_API_KEY": "fixture-key"}
	if err := writeOpenCodeMCPConfig(project, []MCPServer{{
		ID: "compat", Transport: MCPTransportStdio, Command: os.Args[0],
		Args: []string{"-test.run=TestManagedStdioMCPClient"},
		Env:  map[string]string{"PRAIMATE_MCP_TEST_HELPER": "1"},
	}}, env); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"enabled_providers": []string{"praimate-local"}, "model": "praimate-local/compat",
		"small_model": "praimate-local/compat", "snapshot": false,
	}
	raw, _ := json.Marshal(config)
	env["OPENCODE_CONFIG_CONTENT"] = string(raw)
	receiver, err := beginUsageReceiver(ctx, nil, "opencode", "praimate-local/compat", env)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	for key, value := range receiver.Env {
		env[key] = value
	}
	adapter := NewOpenCodeAdapter()
	adapter.bin = bin
	var events []StreamEvent
	emit := func(event StreamEvent) { events = append(events, event) }
	first, err := adapter.SingleShotStream(ctx, SingleShotOpts{
		Cwd: project, Message: "compat-start", Model: "praimate-local/compat", Tools: "full", Env: env,
	}, emit)
	if err != nil || first == nil || first.ExitCode != 0 || first.SessionID == "" || !strings.Contains(first.Text, "compat-start-ok") {
		t.Fatalf("initial turn: reply=%+v err=%v", first, err)
	}
	second, err := adapter.ResumeStream(ctx, first.SessionID, ResumeOpts{
		Cwd: project, Message: "compat-resume", Model: "praimate-local/compat", Tools: "full", Env: env,
	}, emit)
	if err != nil || second == nil || second.ExitCode != 0 || second.SessionID != first.SessionID || !strings.Contains(second.Text, "compat-resume-ok") {
		t.Fatalf("resumed turn: reply=%+v err=%v", second, err)
	}
	body, err := os.ReadFile(filepath.Join(project, "compat.txt"))
	if !mcpRequested.Load() {
		probe := exec.CommandContext(ctx, bin, "mcp", "list")
		probe.Dir = project
		probe.Env = mergeEnv(probe.Environ(), env)
		out, probeErr := probe.CombinedOutput()
		t.Logf("OpenCode fixture MCP status: %v: %s", probeErr, truncate(string(out), 4000))
	}
	if err != nil || string(body) != "compatibility-ok" || !commandRequested.Load() || !mcpRequested.Load() {
		t.Fatalf("tools: command=%v mcp=%v file=%q err=%v", commandRequested.Load(), mcpRequested.Load(), body, err)
	}
	var mcpOK, commandOK, skillOK, usageOK bool
	for _, event := range events {
		if event.Type == "tool_end" && event.OK {
			mcpOK = mcpOK || strings.HasSuffix(event.Tool, "echo")
			commandOK = commandOK || event.Tool == "bash"
			if event.Tool == "skill" {
				part, _ := event.Raw["part"].(map[string]any)
				state, _ := part["state"].(map[string]any)
				skillOK = strings.Contains(stringFromMap(state, "output"), "compat-skill-ok")
			}
		}
		usageOK = usageOK || (event.Usage != nil && event.Usage.PromptTokens == 40 && event.Usage.CompletionTokens == 8)
	}
	if !mcpOK || !commandOK || !skillOK || !usageOK {
		t.Fatalf("events: MCP=%v command=%v skill=%v usage=%v", mcpOK, commandOK, skillOK, usageOK)
	}
	// Plugin delivery is asynchronous; wait briefly for the independent terminal
	// exporter, whose counters must agree with the JSON stream counters.
	deadline := time.Now().Add(2 * time.Second)
	for {
		receiver.captureMu.Lock()
		reports := append([]StreamEvent(nil), receiver.captured...)
		receiver.captureMu.Unlock()
		if len(reports) > 0 {
			if reports[0].Usage.PromptTokens != 40 || reports[0].Usage.CompletionTokens != 8 {
				t.Fatalf("plugin usage: %+v", reports[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("usage plugin did not report completed steps")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Test the real CLI's otherwise silent long quota wait. The launch plugin
	// must stop it while preserving the session for a different model.
	quotaExceeded.Store(true)
	quotaCtx, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	limited, quotaErr := adapter.ResumeStream(quotaCtx, first.SessionID, ResumeOpts{
		Cwd: project, Message: "compat-quota", Model: "praimate-local/compat", Tools: "full", Env: env,
	}, emit)
	if quotaErr == nil || !strings.Contains(quotaErr.Error(), "Free usage exceeded") || !strings.Contains(quotaErr.Error(), "choose another model") || limited == nil || limited.SessionID != first.SessionID {
		t.Fatalf("upstream quota bridge: reply=%+v err=%v", limited, quotaErr)
	}
}

func isolateOpenCodeFixture(t *testing.T, root string) {
	t.Helper()
	// Isolate configuration, sessions, auth, plugins and skills from the user.
	for key, value := range map[string]string{
		"XDG_CONFIG_HOME":    filepath.Join(root, "config"),
		"XDG_DATA_HOME":      filepath.Join(root, "data"),
		"XDG_CACHE_HOME":     filepath.Join(root, "cache"),
		"XDG_STATE_HOME":     filepath.Join(root, "state"),
		"OPENCODE_TEST_HOME": root, "OPENCODE_CONFIG_DIR": filepath.Join(root, "config", "opencode"),
		"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
		"OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_LSP_DOWNLOAD": "1",
		"OPENCODE_CONFIG": "", "OPENCODE_CONFIG_CONTENT": "", "OPENCODE_SERVER_PASSWORD": "",
	} {
		t.Setenv(key, value)
	}
}
