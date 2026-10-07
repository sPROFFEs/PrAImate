package orchestrator

import (
	"testing"
	"time"

	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

func TestAttemptAndUsageSurviveLiveActivityEviction(t *testing.T) {
	run := &Run{}
	now := time.Now().UTC()
	recordWorkerActivity(run, Event{WorkerID: "first", TaskID: "task-a", Tier: Middle, Kind: "started", Text: "Fix A", CLI: "codex", Model: "model-a", Timestamp: now})
	recordWorkerActivity(run, Event{WorkerID: "first", Tier: Middle, Kind: "response", Text: "done", Usage: workerruntime.Usage{Source: "provider", InputTokens: 23, OutputTokens: 7}, Timestamp: now})
	recordWorkerActivity(run, Event{WorkerID: "first", Tier: Middle, Kind: "completed", Timestamp: now})
	for i := 0; i < 600; i++ {
		recordWorkerActivity(run, Event{WorkerID: "second", Tier: Fast, Kind: "tool", Text: "activity", Timestamp: now})
	}
	if len(run.Attempts) != 2 || run.Attempts[0].Status != "completed" || run.Attempts[0].Model != "model-a" {
		t.Fatalf("lost attempt: %+v", run.Attempts)
	}
	if run.Usage.Input != 23 || run.Usage.Output != 7 || run.Usage.Calls != 1 {
		t.Fatalf("lost usage: %+v", run.Usage)
	}
	if len(run.Events) > 500 {
		t.Fatal("live buffer is unbounded")
	}
}
