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
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// Opt-in test of the installed OpenCode/PrAImate Code in the exact worker
// --agent plan path. Inference and configuration are isolated to localhost.
func TestOpenCodeInstalledWorkerCompatibility(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_OPENCODE_BINARY")
	if bin == "" || goruntime.GOOS == "windows" {
		t.Skip("set PRAIMATE_TEST_OPENCODE_BINARY to an installed binary on Unix")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("PRAIMATE_TEST_OPENCODE_BINARY must be absolute")
	}
	for _, cli := range []string{"opencode", "praimate-code"} {
		t.Run(cli, func(t *testing.T) {
			const task = "Inspect the scoped fixture and return one final action."
			const instructions = "Worker instructions: return one JSON action; do not change files."
			const answer = `{"action":"final","content":"fixture findings"}`
			requests := make(chan bool, 8)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/chat/completions" {
					http.NotFound(w, r)
					return
				}
				var request struct {
					Model    string          `json:"model"`
					Messages json.RawMessage `json:"messages"`
				}
				err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request)
				select {
				case requests <- err == nil && request.Model == "fixture-model" && strings.Contains(string(request.Messages), task) && strings.Contains(string(request.Messages), instructions):
				default:
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, chunk := range []map[string]any{
					{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"role": "assistant", "reasoning_content": "Inspecting the scoped fixture."}, "finish_reason": nil}}},
					{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": answer}, "finish_reason": nil}}},
					{"choices": []any{map[string]any{"index": 0, "delta": map[string]string{}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 12, "completion_tokens": 3, "total_tokens": 15}},
				} {
					chunk["id"], chunk["object"], chunk["model"], chunk["created"] = "fixture", "chat.completion.chunk", "fixture-model", 1
					raw, _ := json.Marshal(chunk)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}
				fmt.Fprint(w, "data: [DONE]\n\n")
			}))
			defer backend.Close()
			root := t.TempDir()
			for key, value := range map[string]string{
				"PRAIMATE_HOME": root, "OPENCODE_TEST_HOME": root,
				"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"),
				"XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"),
				"OPENCODE_CONFIG_DIR":         filepath.Join(root, "config", "opencode"),
				"OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1",
				"OPENCODE_DISABLE_DEFAULT_PLUGINS": "1", "OPENCODE_DISABLE_EXTERNAL_SKILLS": "1",
				"OPENCODE_DISABLE_CLAUDE_CODE": "1", "OPENCODE_DISABLE_LSP_DOWNLOAD": "1",
				"OPENCODE_CONFIG": "", "OPENCODE_SERVER_PASSWORD": "", "OPENAI_API_KEY": "",
			} {
				t.Setenv(key, value)
			}
			config := map[string]any{
				"enabled_providers": []string{"praimate-worker-fixture"}, "snapshot": false,
				"model": "praimate-worker-fixture/fixture-model", "small_model": "praimate-worker-fixture/fixture-model",
				"provider": map[string]any{"praimate-worker-fixture": map[string]any{
					"npm": "@ai-sdk/openai-compatible", "name": "Isolated worker fixture",
					"options": map[string]string{"baseURL": backend.URL + "/v1", "apiKey": "fixture-key"},
					"models":  map[string]any{"fixture-model": map[string]string{"name": "fixture-model"}},
				}},
			}
			raw, _ := json.Marshal(config)
			t.Setenv("OPENCODE_CONFIG_CONTENT", string(raw))
			t.Setenv("PRAIMATE_WORKER_FIXTURE_BINARY", bin)
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(binDir, cli), []byte("#!/bin/sh\nexec \"$PRAIMATE_WORKER_FIXTURE_BINARY\" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("PWD", root)
			var adapter core.CLIAdapter = core.NewOpenCodeAdapter()
			if cli == "praimate-code" {
				adapter = core.NewPraimateCodeAdapter()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			var progress []ProgressEvent
			result, err := (CLI{Adapter: adapter}).Execute(ctx, Request{Model: "praimate-worker-fixture/fixture-model", Task: task, SystemPrompt: instructions, WorkspaceRoot: t.TempDir(), Progress: func(e ProgressEvent) { progress = append(progress, e) }})
			if err != nil || result == nil || result.Content != answer || result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 {
				t.Fatalf("installed %s worker failed: %+v %v", cli, result, err)
			}
			select {
			case delivered := <-requests:
				if !delivered {
					t.Fatal("worker task/model/instructions did not reach the backend")
				}
			default:
				t.Fatal("worker did not reach the local backend")
			}
			var started, reasoning bool
			for _, e := range progress {
				started = started || e.Kind == "backend_status"
				reasoning = reasoning || e.Kind == "reasoning" && strings.Contains(e.Text, "Inspecting the scoped fixture.")
			}
			if !started || !reasoning {
				t.Fatalf("installed worker lost lifecycle/reasoning: %+v", progress)
			}
		})
	}
}
