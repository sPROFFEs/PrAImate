package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func TestOpenCodeInstalledWorkerAppliesEditsInsteadOfPlanning(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_OPENCODE_BINARY")
	if bin == "" || goruntime.GOOS == "windows" {
		t.Skip("set PRAIMATE_TEST_OPENCODE_BINARY on Unix")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("binary path must be absolute")
	}
	root, workspace := t.TempDir(), t.TempDir()
	for key, value := range map[string]string{
		"PRAIMATE_HOME": root, "OPENCODE_TEST_HOME": root, "XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"), "OPENCODE_CONFIG_DIR": filepath.Join(root, "config", "opencode"), "OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1", "OPENCODE_DISABLE_DEFAULT_PLUGINS": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1", "OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_LSP_DOWNLOAD": "1", "OPENCODE_CONFIG": "", "OPENCODE_SERVER_PASSWORD": "", "OPENAI_API_KEY": "",
	} {
		t.Setenv(key, value)
	}
	path := filepath.Join(workspace, "fixture.txt")
	if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		n := calls.Add(1)
		delta := map[string]any{"content": `{"action":"final","content":"file updated"}`}
		finish := "stop"
		if n <= 2 {
			name := "read"
			args := map[string]any{"filePath": path}
			if n == 2 {
				name = "edit"
				args["oldString"] = "before"
				args["newString"] = "after"
			}
			encoded, _ := json.Marshal(args)
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("call-%d", n), "type": "function", "function": map[string]string{"name": name, "arguments": string(encoded)}}}}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, chunk := range []map[string]any{
			{"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}},
			{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}},
		} {
			chunk["id"], chunk["object"], chunk["model"], chunk["created"] = "fixture", "chat.completion.chunk", "fixture-model", 1
			raw, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", raw)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer backend.Close()
	config := map[string]any{"default_agent": "plan", "enabled_providers": []string{"praimate-worker-fixture"}, "snapshot": false, "model": "praimate-worker-fixture/fixture-model", "small_model": "praimate-worker-fixture/fixture-model", "provider": map[string]any{"praimate-worker-fixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Isolated edit fixture", "options": map[string]string{"baseURL": backend.URL + "/v1", "apiKey": "fixture-key"}, "models": map[string]any{"fixture-model": map[string]string{"name": "fixture-model"}}}}}
	raw, _ := json.Marshal(config)
	t.Setenv("OPENCODE_CONFIG_CONTENT", string(raw))
	t.Setenv("PRAIMATE_WORKER_FIXTURE_BINARY", bin)
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "opencode"), []byte("#!/bin/sh\nexec \"$PRAIMATE_WORKER_FIXTURE_BINARY\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PWD", root)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	result, err := (CLI{Adapter: core.NewOpenCodeAdapter(), AllowEdits: true}).Execute(ctx, Request{Model: "praimate-worker-fixture/fixture-model", Task: "Read fixture.txt, replace before with after, then return a final JSON action.", SystemPrompt: "Apply the assigned file edit and report the outcome.", WorkspaceRoot: workspace})
	if err != nil || result == nil {
		t.Fatalf("worker execution: %+v %v", result, err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "after\n" {
		t.Fatalf("worker planned instead of editing: content=%q result=%+v err=%v", content, result, err)
	}
	if calls.Load() < 3 {
		t.Fatal("native read/edit cycle did not execute")
	}
}
