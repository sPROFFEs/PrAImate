package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestWorkerJournalPaginationAndAtomicSnapshot(t *testing.T) {
	ctx := context.Background()
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	id := "worker-journal"
	if _, err = c.CreateChat(ctx, CreateChatRequest{ID: id, Title: "Journal", CLIAgent: "workers", Settings: ChatSettings{Surface: "workers"}}); err != nil {
		t.Fatal(err)
	}
	_, _ = c.AddMessage(ctx, id, "system", `{"workspace":"fixture"}`, nil)
	items := []json.RawMessage{}
	for i := 1; i <= 205; i++ {
		items = append(items, json.RawMessage(fmt.Sprintf(`{"sequence":%d,"workerID":"a","kind":"tool"}`, i)))
	}
	items = append(items, json.RawMessage(`{"sequence":206,"workerID":"b"}`))
	if err = c.SaveWorkerRunSnapshot(ctx, id, `{"id":"worker-journal","status":"failed"}`, items); err != nil {
		t.Fatal(err)
	}
	first, err := c.WorkerActivity(ctx, id, "a", 0, 100)
	if err != nil || len(first.Items) != 100 || !first.HasMore {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := c.WorkerActivity(ctx, id, "a", first.Before, 200)
	if err != nil || len(second.Items) != 105 || second.HasMore {
		t.Fatalf("second page: %+v %v", second, err)
	}
	var oldest struct {
		Sequence int `json:"sequence"`
	}
	_ = json.Unmarshal(second.Items[0], &oldest)
	if oldest.Sequence != 1 {
		t.Fatal("activity is not chronological")
	}
	if err = c.SaveWorkerRunSnapshot(ctx, id, `{"id":"worker-journal","status":"completed"}`, []json.RawMessage{json.RawMessage(`{"workerID":"c"}`), json.RawMessage(`broken`)}); err == nil {
		t.Fatal("invalid journal accepted")
	}
	_, saved, err := c.LoadWorkerRun(ctx, id)
	if err != nil || string(saved) != `{"id":"worker-journal","status":"failed"}` {
		t.Fatalf("snapshot changed after rollback: %s %v", saved, err)
	}
	rolledBack, _ := c.WorkerActivity(ctx, id, "c", 0, 100)
	if len(rolledBack.Items) != 0 {
		t.Fatal("partial journal was committed")
	}
	// A bounded 64 KiB output can occupy six times as much in encoded JSON.
	text := strings.Repeat("<", 64<<10)
	encoded, _ := json.Marshal(map[string]any{"workerID": "escaped", "text": text})
	if err = c.SaveWorkerRunSnapshot(ctx, id, `{"id":"worker-journal","status":"completed"}`, []json.RawMessage{encoded}); err != nil {
		t.Fatalf("valid bounded output rejected after JSON escaping: %v", err)
	}
	page, err := c.WorkerActivity(ctx, id, "escaped", 0, 100)
	var event struct{ Text string }
	if err != nil || len(page.Items) != 1 || json.Unmarshal(page.Items[0], &event) != nil || event.Text != text {
		t.Fatalf("escaped output was not retained: %v", err)
	}
}
