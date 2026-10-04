package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

type deadlineWorker struct {
	fakeWorker
	deadline bool
}

func TestActivityDoesNotMixParallelInvocationsAndStaysBounded(t *testing.T) {
	run := &Run{}
	now := time.Now().UTC()
	for _, id := range []string{"a", "b", "a"} {
		recordWorkerActivity(run, Event{WorkerID: id, Tier: Middle, Kind: "stream", Text: "delta", Timestamp: now})
	}
	if len(run.Events) != 3 {
		t.Fatal("combined different worker invocations")
	}
	recordWorkerActivity(run, Event{WorkerID: "a", Tier: Middle, Kind: "stream", Text: "next", Timestamp: now.Add(time.Second)})
	if run.Events[2].Text != "deltanext" || !run.Events[2].Timestamp.Equal(now.Add(time.Second)) {
		t.Fatal("stream lost text or last progress time")
	}
	for i := 0; i < 600; i++ {
		recordWorkerActivity(run, Event{Kind: "response", Text: strings.Repeat("x", 4096)})
	}
	total := 0
	for _, e := range run.Events {
		total += len(e.Text)
	}
	if len(run.Events) > 500 || total > 1<<20 {
		t.Fatalf("unbounded activity: %d events, %d bytes", len(run.Events), total)
	}
}

func (w *deadlineWorker) Execute(ctx context.Context, req workerruntime.Request) (*workerruntime.Result, error) {
	if w.deadline {
		return nil, context.DeadlineExceeded
	}
	if _, bounded := ctx.Deadline(); bounded {
		return nil, errors.New("unexpected global worker deadline")
	}
	return &workerruntime.Result{Content: `{"action":"final","content":"done"}`}, nil
}

func TestWorkerDeadlineReportsRouteAndPhaseAndRemainsInspectable(t *testing.T) {
	config := testConfig(t)
	var events []Event
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return &deadlineWorker{deadline: true}, nil }, Emit: func(e Event) { events = append(events, e) }}
	_, err := runner.Plan(context.Background(), config, "draft a plan")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "model-primary") || !strings.Contains(err.Error(), "10s") || !strings.Contains(err.Error(), "planning") {
		t.Fatalf("missing diagnostics: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != "failed" || last.WorkerID == "" || last.Model != "model-primary" || last.TimeoutSeconds != 10 {
		t.Fatalf("missing failure provenance: %+v", last)
	}
}

func TestBackendDeadlineDoesNotInventAHostTimeout(t *testing.T) {
	err := workerError(context.Background(), Profile{Tier: Primary, Runtime: "cli", CLI: "codex", Model: "test", TimeoutSeconds: 0}, "planning", context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "host call timeout is disabled") || strings.Contains(err.Error(), "0s") {
		t.Fatalf("invented a host timeout: %v", err)
	}
}

func TestWorkerRunDoesNotImposeHiddenGlobalDeadline(t *testing.T) {
	config := testConfig(t)
	for i := range config.Profiles {
		config.Profiles[i].TimeoutSeconds = 0
	}
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return &deadlineWorker{}, nil }}
	if _, err := runner.Run(context.Background(), config, "long operation"); err != nil {
		t.Fatal(err)
	}
	config.Profiles[0].TimeoutSeconds = 3600
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	config.Profiles[0].TimeoutSeconds = -1
	if config.Validate() == nil {
		t.Fatal("negative timeout accepted")
	}
}

func TestWorkerDelegationRecordsSeparateInvocationsAndParents(t *testing.T) {
	workers := map[Tier]*fakeWorker{
		Primary: {responses: []string{`{"action":"delegate","tier":"fast","task":"find symbol"}`, `{"action":"final","content":"done"}`}},
		Fast:    {responses: []string{`{"action":"final","content":"found"}`}},
	}
	var events []Event
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) { return workers[p.Tier], nil }, Emit: func(e Event) { events = append(events, e) }}
	if _, err := runner.Run(context.Background(), testConfig(t), "review"); err != nil {
		t.Fatal(err)
	}
	parent := ""
	for _, event := range events {
		if event.Kind == "started" && event.Tier == Primary {
			parent = event.WorkerID
		}
		if event.Kind == "started" && event.Tier == Fast && (event.ParentID != parent || event.WorkerID == parent || event.Model != "model-fast") {
			t.Fatalf("invalid child provenance: %+v", event)
		}
		if event.Kind == "delegation" && event.Target != Fast {
			t.Fatalf("missing target: %+v", event)
		}
	}
	if parent == "" {
		t.Fatal("no parent invocation recorded")
	}
}
