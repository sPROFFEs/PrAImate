package orchestrator

import (
	"time"
	"unicode/utf8"
)

// SubscribeChanges is a transport-neutral invalidation feed. Consumers fetch
// current state; a slow subscriber can never stall model execution.
func (m *Manager) SubscribeChanges() (<-chan string, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan string, 64)
	if m.subscribers == nil {
		m.subscribers = map[chan string]struct{}{}
	}
	m.subscribers[ch] = struct{}{}
	return ch, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, ok := m.subscribers[ch]; ok {
			delete(m.subscribers, ch)
			close(ch)
		}
	}
}
func (m *Manager) notifyLocked(id string) {
	for ch := range m.subscribers {
		select {
		case ch <- id:
		default:
		}
	}
}
func (m *Manager) Progress(id string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return map[string]any{"id": id, "deleted": true}
	}
	out := map[string]any{"id": id, "status": run.Status, "sequence": run.EventSequence, "updatedAt": run.UpdatedAt.Format(time.RFC3339Nano)}
	if len(run.Events) > 0 {
		event := run.Events[len(run.Events)-1]
		if len(event.Text) > 4096 {
			event.Text = event.Text[len(event.Text)-4096:]
			for len(event.Text) > 0 && !utf8.RuneStart(event.Text[0]) {
				event.Text = event.Text[1:]
			}
		}
		out["activity"] = event
	}
	return out
}
