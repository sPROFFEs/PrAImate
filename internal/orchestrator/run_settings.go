package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// UpdateRunConfig affects subsequent invocations, never a running process or
// the task's recorded attempts/results. Tasks inherit updated profiles unless
// their requested route explicitly overrides them. Workspace identity is fixed.
func (m *Manager) UpdateRunConfig(id string, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if m.closed || run == nil {
		return errors.New("worker run not found")
	}
	if run.cancel != nil {
		return errors.New("stop the execution before changing its worker profiles")
	}
	if run.Workspace != config.Workspace {
		return errors.New("an existing worker run's workspace cannot be changed")
	}
	previous := *run
	run.Profiles = append([]Profile(nil), config.Profiles...)
	config.Profiles = run.Profiles
	run.config = config
	run.UpdatedAt = time.Now().UTC()
	var err error
	if run.DAG != nil {
		err = m.saveDAGLocked(run)
	} else {
		var raw []byte
		raw, err = json.Marshal(run)
		if err == nil {
			_, err = m.core.AddMessage(m.ctx, id, "assistant", string(raw), nil)
		}
	}
	if err != nil {
		*run = previous
		return err
	}
	return nil
}

// UseDAGTaskProfile removes task-specific routing for its next attempt. Recorded
// attempts and retained worktrees remain intact; resetting failed work is separate.
func (m *Manager) UseDAGTaskProfile(id, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if m.closed || run == nil || run.DAG == nil {
		return errors.New("task graph unavailable")
	}
	if run.cancel != nil {
		return errors.New("stop execution before changing a task's worker route")
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.ID != taskID {
			continue
		}
		if task.Status != "pending" && task.Status != "failed" && task.Status != "cancelled" && task.Status != "blocked" {
			return errors.New("only pending or unsuccessful tasks can change their worker route")
		}
		request := WorkerConfig{ProfileID: task.requestedWorker().ProfileID}
		if _, _, err := resolveTaskProfile(run.config, request); err != nil {
			return err
		}
		previous := cloneTask(*task)
		task.RequestedWorker = &request
		if run.Status == "draft" {
			task.Worker = request
		}
		if err := m.saveDAGLocked(run); err != nil {
			*task = previous
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

// RetryPlanning is explicit and only repeats a read-only planning call. Once
// tasks exist, users must use their individual review/reset controls instead.
func (m *Manager) RetryPlanning(id string) error {
	m.mu.Lock()
	run := m.runs[id]
	if m.closed || run == nil || run.DAG == nil {
		m.mu.Unlock()
		return errors.New("task graph unavailable")
	}
	if run.cancel != nil || len(run.DAG.Tasks) != 0 {
		m.mu.Unlock()
		return errors.New("only an inactive plan without tasks can be retried")
	}
	if err := m.checkDAGCapacityLocked(); err != nil {
		m.mu.Unlock()
		return err
	}
	config, objective := run.config, run.Task
	previous := *run
	ctx, cancel := context.WithCancel(m.ctx)
	run.cancel, run.Status, run.Error = cancel, "planning", ""
	if err := m.saveDAGLocked(run); err != nil {
		*run = previous
		cancel()
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.plan(ctx, cancel, id, config, objective)
	return nil
}
