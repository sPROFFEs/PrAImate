package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

func (m *Manager) saveRunLocked(run *Run) error {
	run.UpdatedAt = time.Now().UTC()
	run.ActivityVersion = 1
	activity := make([]json.RawMessage, 0, len(run.pendingEvents))
	for _, event := range run.pendingEvents {
		body, err := json.Marshal(event)
		if err != nil {
			return err
		}
		activity = append(activity, body)
	}
	body, err := json.Marshal(run)
	if err == nil {
		err = m.core.SaveWorkerRunSnapshot(context.Background(), run.ID, string(body), activity)
	}
	if err != nil {
		run.Error = "Could not save worker execution: " + err.Error()
		return err
	}
	run.pendingEvents = nil
	run.lastCheckpoint = run.UpdatedAt
	return nil
}

func checkpointActivity(run *Run, event Event) bool {
	return event.Kind == "started" || event.Kind == "completed" || event.Kind == "failed" || event.Kind == "cancelled" || event.Kind == "session" || event.Kind == "retry_scheduled" || len(run.pendingEvents) >= 128 || time.Since(run.lastCheckpoint) >= 10*time.Second
}

type ActivityPage struct {
	Events  []Event `json:"events"`
	Before  int64   `json:"before"`
	HasMore bool    `json:"hasMore"`
}

func (m *Manager) Activity(id, workerID string, before int64, limit int) (ActivityPage, error) {
	m.mu.Lock()
	_, ok := m.runs[id]
	m.mu.Unlock()
	page := ActivityPage{Events: []Event{}}
	if !ok || m.core == nil {
		return page, errors.New("worker execution not found")
	}
	saved, err := m.core.WorkerActivity(m.ctx, id, workerID, before, limit)
	if err != nil {
		return page, err
	}
	page.Before, page.HasMore = saved.Before, saved.HasMore
	for _, item := range saved.Items {
		var event Event
		if err = json.Unmarshal(item, &event); err != nil {
			return page, err
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
}
