package core

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/agentic"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

type usageStreamAdapter struct{ *mockAdapter }

func (a usageStreamAdapter) SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	emit(StreamEvent{Type: "usage", ID: "report", Model: "reported-model", Usage: &NativeUsage{120, 40, 160}})
	return a.SingleShot(ctx, opts)
}
func (a usageStreamAdapter) ResumeStream(ctx context.Context, id string, opts ResumeOpts, emit StreamHandler) (*Reply, error) {
	return nil, errors.New("unexpected resume")
}

func TestUsageSurvivesManagedModelEventBridge(t *testing.T) {
	model := managedCLIModel{adapter: usageStreamAdapter{&mockAdapter{name: "usage-stream", replies: []string{`{"action":"finish","message":"done"}`}}}, cwd: t.TempDir()}
	var usage UsageAccumulator
	_, err := model.Turn(context.Background(), agentic.ModelInput{Message: "task"}, func(e agentic.Event) {
		usage.Observe(managedStreamEvent(ManagedRunEvent{Type: e.Type, Payload: e.Payload}))
	})
	if err != nil {
		t.Fatal(err)
	}
	in, out, calls, name := usage.Snapshot()
	if in != 120 || out != 40 || calls != 1 || name != "reported-model" {
		t.Fatalf("managed usage lost: %d %d %d %s", in, out, calls, name)
	}
}

func TestUsageDashboardAggregatesOnlyReportedUsageAcrossMonthBoundaries(t *testing.T) {
	c := nativeTestCore(t)
	ctx := context.Background()
	for _, row := range []struct {
		date, cli, model string
		usage            *NativeUsage
	}{
		{"2026-09-01T00:00:00.123Z", "codex", "reasoner", &NativeUsage{100, 50, 150}},
		{"2026-09-30T23:59:59.999Z", "codex", "reasoner", nil},
		{"2026-09-20T10:00:00Z", "claude", "medium", &NativeUsage{0, 0, 0}},
		{"2026-10-01T00:00:00Z", "outside", "other", &NativeUsage{999, 999, 1998}},
	} {
		u, err := c.BeginUsage(ctx, row.cli, row.model, "chat")
		if err != nil {
			t.Fatal(err)
		}
		u.Observe(StreamEvent{Type: "usage", ID: "same-provider-event", Usage: row.usage})
		u.Observe(StreamEvent{Type: "usage", ID: "same-provider-event", Usage: row.usage})
		if err := u.Finish(nil); err != nil {
			t.Fatal(err)
		}
		if _, err := c.store.DB().Exec(`UPDATE usage_runs SET started_at=? WHERE id=?`, row.date, u.id); err != nil {
			t.Fatal(err)
		}
	}
	d, err := c.UsageDashboard(ctx, "2026-09")
	if err != nil {
		t.Fatal(err)
	}
	if d.Totals.Runs != 3 || d.Totals.ReportedRuns != 2 || d.Totals.Tokens != 150 || d.Totals.AverageTokensPerRun != 75 || d.Totals.ActiveDays != 3 {
		t.Fatalf("incorrect totals: %+v", d)
	}
	if len(d.CLIs) != 2 || d.CLIs[0].Name != "codex" || d.CLIs[0].Runs != 2 || len(d.Months) != 1 {
		t.Fatalf("incorrect buckets: %+v", d)
	}
	if _, err := c.UsageDashboard(ctx, "2026-13"); err == nil {
		t.Fatal("invalid month accepted")
	}
}

func TestUsagePersistsInsideEncryptedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metrics.sqlite")
	s, err := store.InitializeWithPassword(path, "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.BeginUsage(context.Background(), "private-cli-marker", "private-model-marker", "workers")
	if err != nil {
		t.Fatal(err)
	}
	u.Observe(StreamEvent{Type: "usage", Usage: &NativeUsage{12, 8, 20}})
	if err := u.Finish(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"SQLite format 3", "private-cli-marker", "private-model-marker", "usage_runs"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("plaintext metric/schema in database: %s", secret)
		}
	}
}

func TestUsageStreamParsersNormalizeProviderTotals(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		parse      func(string, StreamHandler)
	}{
		{"codex", `{"type":"turn.completed","usage":{"input_tokens":120,"cached_input_tokens":50,"output_tokens":40}}`, func(s string, e StreamHandler) { parseCodexStream(strings.NewReader(s), e) }},
		{"claude", `{"type":"result","result":"ok","usage":{"input_tokens":120,"output_tokens":40}}`, func(s string, e StreamHandler) { _, _ = parseClaudeStream(strings.NewReader(s), e) }},
		{"opencode", `{"type":"step_finish","part":{"id":"report","type":"step-finish","tokens":{"input":120,"output":10,"reasoning":30}}}`, func(s string, e StreamHandler) { _, _ = parseOpenCodeStream(strings.NewReader(s), e) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var u UsageAccumulator
			tc.parse(tc.body, u.Observe)
			in, out, calls, _ := u.Snapshot()
			if in != 120 || out != 40 || calls != 1 {
				t.Fatalf("input=%d output=%d reports=%d", in, out, calls)
			}
		})
	}
	if reportedUsage(map[string]any{"input": float64(5)}, "input", "output") != nil {
		t.Fatal("invented missing output usage")
	}
}

func TestClaudeUsageIncludesCacheAndSurvivesMissingResult(t *testing.T) {
	message := `{"type":"assistant","message":{"id":"m1","model":"test","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40},"content":[]}}`
	for _, final := range []bool{false, true} {
		body := message + "\n" + message + "\n"
		if final {
			body += `{"type":"result","result":"ok","usage":{"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":40}}`
		}
		var u UsageAccumulator
		_, _ = parseClaudeStream(strings.NewReader(body), u.Observe)
		in, out, calls, _ := u.Snapshot()
		if in != 80 || out != 20 || calls != 1 {
			t.Fatalf("final=%v got %d/%d/%d", final, in, out, calls)
		}
	}
}
