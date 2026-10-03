package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Codex's real exporter must resolve the launch header and deliver counters to
// the encrypted dashboard. All model responses come from localhost, with no
// account or subscription. Ordinary tests do not require an installed Codex.
func TestCodexInstalledTerminalUsageCompatibility(t *testing.T) {
	bin := os.Getenv("PRAIMATE_TEST_CODEX_BINARY")
	if bin == "" {
		t.Skip("set PRAIMATE_TEST_CODEX_BINARY to test an installed Codex version")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("PRAIMATE_TEST_CODEX_BINARY must be absolute")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	version := exec.CommandContext(ctx, bin, "--version")
	if output, err := version.CombinedOutput(); err != nil {
		t.Fatalf("Codex version: %v: %s", err, output)
	} else {
		t.Logf("%s", strings.TrimSpace(string(output)))
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			http.NotFound(w, r)
			return
		}
		// Discard the request; neither prompts nor credentials enter fixtures.
		_, _ = io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, 2<<20))
		message := map[string]any{"id": "msg-usage", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "ok", "annotations": []any{}}}}
		response := map[string]any{"id": "resp-usage", "object": "response", "model": "praimate-usage-fixture", "status": "completed", "output": []any{message}, "usage": map[string]any{"input_tokens": 120, "output_tokens": 40, "total_tokens": 160, "input_tokens_details": map[string]any{"cached_tokens": 50}, "output_tokens_details": map[string]any{"reasoning_tokens": 10}}}
		events := []map[string]any{
			{"type": "response.created", "response": map[string]any{"id": "resp-usage", "object": "response", "status": "in_progress", "output": []any{}}},
			{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "msg-usage", "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}},
			{"type": "response.output_text.delta", "item_id": "msg-usage", "output_index": 0, "content_index": 0, "delta": "ok"},
			{"type": "response.output_item.done", "output_index": 0, "item": message},
			{"type": "response.completed", "response": response},
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			body, _ := json.Marshal(event)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], body)
		}
	}))
	defer backend.Close()
	c := nativeTestCore(t)
	u, err := c.BeginTerminalUsage(ctx, "codex", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	workspace := t.TempDir()
	args := []string{"exec", "--ignore-user-config", "--ignore-rules", "--ephemeral", "--skip-git-repo-check", "--json", "--sandbox", "read-only", "-C", workspace, "-m", "praimate-usage-fixture"}
	state, _ := json.Marshal(filepath.Join(workspace, "state"))
	provider := fmt.Sprintf(`model_providers.praimate_usage_fixture={name="PrAImate usage fixture",base_url="%s/v1",wire_api="responses",requires_openai_auth=false,supports_websockets=false}`, backend.URL)
	for _, value := range []string{`cli_auth_credentials_store="ephemeral"`, `history.persistence="none"`, "sqlite_home=" + string(state), "check_for_update_on_startup=false", "analytics.enabled=false", "features.apps=false", "mcp_servers={}", `model_provider="praimate_usage_fixture"`, provider, `otel.metrics_exporter="none"`, `otel.trace_exporter="none"`, `otel.exporter.otlp-http.headers.Authorization="previous-exporter-fixture"`} {
		args = append(args, "-c", value)
	}
	args = append(args, u.Args...)
	args = append(args, "Reply ok.")
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workspace
	cmd.Env = mergeEnv(os.Environ(), u.Env)
	cmd.Stdout = io.Discard
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		t.Fatalf("Codex fixture: %v: %s", err, truncate(stderr.String(), 1200))
	}
	// Codex flushes its exporter before exiting. Cached/reasoning subtotals
	// must not be added again to the provider's complete input/output totals.
	dashboard, err := c.UsageDashboard(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if dashboard.Totals.InputTokens != 120 || dashboard.Totals.OutputTokens != 40 || dashboard.Totals.Tokens != 160 || dashboard.Totals.ReportedRuns != 1 {
		t.Fatalf("Codex reports did not reach the dashboard exactly once: %+v", dashboard.Totals)
	}
	if len(dashboard.CLIs) != 1 || dashboard.CLIs[0].Name != "codex" || len(dashboard.Models) != 1 || dashboard.Models[0].Name != "praimate-usage-fixture" {
		t.Fatalf("Codex/model attribution missing: CLIs=%+v models=%+v", dashboard.CLIs, dashboard.Models)
	}
}
