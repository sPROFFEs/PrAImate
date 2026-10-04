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

// Exercise the installed CLI's real stdin/JSONL path against localhost only.
// A clean CODEX_HOME prevents reading account credentials or user settings.
func TestCodexInstalledWorkerPlanningCompatibility(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODEX_BINARY")
	if bin == "" || goruntime.GOOS == "windows" {
		t.Skip("set PRAIMATE_TEST_CODEX_BINARY to an installed Codex binary on Unix")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("PRAIMATE_TEST_CODEX_BINARY must be absolute")
	}
	const task = "Draft the fixture plan; do not run commands."
	const plan = `{"tasks":[{"id":"fixture-task","description":"Inspect a fixture file","dependencies":[],"worker":{"profile":"fast"}}]}`
	const summary = "Creating concrete scoped tasks."
	requests := make(chan bool, 8)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		var request struct {
			Model     string          `json:"model"`
			Input     json.RawMessage `json:"input"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request)
		select {
		case requests <- err == nil && request.Model == "gpt-6.1-sol" && strings.Contains(string(request.Input), task) && request.Reasoning.Effort == "medium":
		default:
		}
		reasoning := map[string]any{"id": "rs-fixture", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": summary}}}
		message := map[string]any{"id": "msg-fixture", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": plan, "annotations": []any{}}}}
		response := map[string]any{"id": "resp-fixture", "object": "response", "model": "gpt-6.1-sol", "status": "completed", "output": []any{reasoning, message}, "usage": map[string]any{"input_tokens": 120, "output_tokens": 40, "total_tokens": 160}}
		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp-fixture", "object": "response", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs-fixture", "type": "reasoning", "summary": []any{}}},
			{"type": "response.reasoning_summary_part.added", "item_id": "rs-fixture", "output_index": 0, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}},
			{"type": "response.reasoning_summary_text.delta", "item_id": "rs-fixture", "output_index": 0, "summary_index": 0, "delta": summary},
			{"type": "response.reasoning_summary_text.done", "item_id": "rs-fixture", "output_index": 0, "summary_index": 0, "text": summary},
			{"type": "response.output_item.done", "output_index": 0, "item": reasoning},
			{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "msg-fixture", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "item_id": "msg-fixture", "output_index": 1, "content_index": 0, "delta": plan},
			{"type": "response.output_item.done", "output_index": 1, "item": message},
			{"type": "response.completed", "response": response},
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			body, _ := json.Marshal(event)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], body)
		}
	}))
	defer backend.Close()
	root := t.TempDir()
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("CODEX_API_KEY", "")
	t.Setenv("PRAIMATE_CODEX_FIXTURE_PROVIDER", fmt.Sprintf(`model_providers.praimate_worker_fixture={name="Worker fixture",base_url="%s/v1",wire_api="responses",requires_openai_auth=false,supports_websockets=false}`, backend.URL))
	wrapper := `#!/bin/sh
command="$1"
shift
exec "$PRAIMATE_TEST_CODEX_BINARY" "$command" --ignore-user-config --ignore-rules --ephemeral -c "$PRAIMATE_CODEX_FIXTURE_PROVIDER" -c 'model_provider="praimate_worker_fixture"' -c 'mcp_servers={}' -c 'check_for_update_on_startup=false' -c 'analytics.enabled=false' -c 'features.apps=false' -c 'otel.metrics_exporter="none"' -c 'otel.trace_exporter="none"' "$@"
`
	if err := os.WriteFile(filepath.Join(root, "codex"), []byte(wrapper), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var progress []ProgressEvent
	result, err := (CLI{Adapter: core.NewCodexAdapter()}).Execute(ctx, Request{Model: "gpt-6.1-sol", ReasoningEffort: "medium", Task: task, SystemPrompt: "Return the task graph as JSON only.", WorkspaceRoot: workspace, Progress: func(e ProgressEvent) { progress = append(progress, e) }})
	if err != nil || result == nil || result.Content != plan || result.Usage.InputTokens != 120 || result.Usage.OutputTokens != 40 {
		t.Fatalf("installed Codex worker failed: %+v %v", result, err)
	}
	select {
	case delivered := <-requests:
		if !delivered {
			t.Fatal("worker task/model/effort did not reach the local backend")
		}
	default:
		t.Fatal("worker did not reach the local backend")
	}
	var started, reasoningReported bool
	for _, event := range progress {
		started = started || event.Kind == "backend_status"
		reasoningReported = reasoningReported || event.Kind == "reasoning" && strings.Contains(event.Text, summary)
	}
	if !started || !reasoningReported {
		t.Fatalf("installed Codex lifecycle/reasoning was lost: %+v", progress)
	}
}
