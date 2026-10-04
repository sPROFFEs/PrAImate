package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestWorkerWindowRPCRejectsOtherRunsAndNonWorkerWindows(t *testing.T) {
	d := newDetachedCoordinator(&App{})
	for _, method := range []string{"worker.snapshot", "worker.approvals", "worker.cancel", "worker.config.update", "worker.plan.retry", "worker.graph.execute", "worker.graph.merge"} {
		body, _ := json.Marshal(workerWindowRequest{ID: "other-run"})
		if _, err := d.call(&detachedWindow{kind: "workers", sessionID: "my-run"}, detachedRPCRequest{Method: method, Body: body}); err == nil {
			t.Fatalf("%s escaped run scope", method)
		}
		body, _ = json.Marshal(workerWindowRequest{ID: "my-run"})
		if _, err := d.call(&detachedWindow{kind: "chat", sessionID: "my-run"}, detachedRPCRequest{Method: method, Body: body}); err == nil {
			t.Fatalf("%s accepted non-worker window", method)
		}
	}
}

func TestCancelledWorkerApprovalRemovesPendingSnapshotAndNotifiesUI(t *testing.T) {
	started := make(chan ApprovalRequest, 1)
	finished := make(chan ApprovalRequest, 1)
	b := &approvalBroker{pending: map[string]chan approvalDecision{}, scopes: map[string]string{}, always: map[string]map[string]bool{}, emit: func(r ApprovalRequest) { started <- r }, resolved: func(r ApprovalRequest) { finished <- r }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go b.request(ctx, "workers-test", "command.run", nil)
	r := <-started
	app := &App{approval: b}
	if len(app.WorkerRunApprovals(r.ChatID)) != 1 {
		t.Fatal("pending approval not visible")
	}
	cancel()
	select {
	case done := <-finished:
		if done.ID != r.ID || len(app.WorkerRunApprovals(r.ChatID)) != 0 {
			t.Fatal("cancelled approval remained visible")
		}
	case <-time.After(time.Second):
		t.Fatal("UI not notified of cancelled approval")
	}
}

func TestWorkerApprovalSnapshotDoesNotExposeOtherScopes(t *testing.T) {
	app := &App{approval: &approvalBroker{requests: map[string]ApprovalRequest{
		"one": {ID: "one", ChatID: "my-run", Tool: "command.run", Detail: "echo test"},
		"two": {ID: "two", ChatID: "other-run", Tool: "command.run", Detail: "other"},
	}}}
	requests := app.WorkerRunApprovals("my-run")
	if len(requests) != 1 || requests[0].ID != "one" {
		t.Fatalf("invalid approval snapshot: %+v", requests)
	}
}

func TestDetachedWorkerModeUsesExistingScopedBroker(t *testing.T) {
	t.Setenv("PRAIMATE_DETACHED_KIND", "workers")
	t.Setenv("PRAIMATE_DETACHED_SESSION", "worker-one")
	t.Setenv("PRAIMATE_DETACHED_WINDOW", "00112233445566778899aabb")
	t.Setenv("PRAIMATE_DETACHED_BROKER", "http://127.0.0.1:1234")
	t.Setenv("PRAIMATE_DETACHED_TOKEN", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	mode := detachedModeFromEnvironment()
	if !mode.active || mode.kind != "workers" || mode.sessionID != "worker-one" {
		t.Fatalf("worker window rejected: %+v", mode)
	}
}
