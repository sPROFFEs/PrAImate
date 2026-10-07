package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func retryDelay(config Config, number int) time.Duration {
	seconds := config.RetryDelaySeconds * (1 << min(max(number-1, 0), 4))
	return time.Duration(min(seconds, 60)) * time.Second
}
func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
func canAutomaticallyRetry(ctx context.Context, message string) bool {
	if ctx.Err() != nil {
		return false
	}
	// A declined permission is an instruction to stop, not a transient failure.
	lower := strings.ToLower(message)
	for _, marker := range []string{"denied by the user", "requires an interactive user approval", "not allowed", "cannot enforce the selected permission", "could not save worker execution"} {
		if strings.Contains(lower, marker) {
			return false
		}
	}
	return true
}
func (m *Manager) retryEvent(id, taskID string, tier Tier, number, limit int, next time.Time, cause string) {
	m.mu.Lock()
	var event Event
	if run := m.runs[id]; run != nil {
		for i := len(run.Attempts) - 1; i >= 0; i-- {
			a := run.Attempts[i]
			if a.TaskID == taskID && (taskID != "" || a.ParentID == "" && a.Tier == tier) {
				event.WorkerID = a.ID
				event.CLI = a.CLI
				event.Model = a.Model
				break
			}
		}
	}
	m.mu.Unlock()
	event.TaskID, event.Tier, event.Kind, event.Timestamp = taskID, tier, "retry_scheduled", time.Now().UTC()
	event.Text = fmt.Sprintf("Automatic retry %d/%d at %s. Existing changes are retained. Previous error: %s", number, limit, next.Format(time.RFC3339), truncateWorkerText(cause, 2048))
	m.recordDAGEvent(id, event)
}

func (m *Manager) executeDAGTaskWithRetries(ctx context.Context, id string, config Config, base string, task DAGTask, deps []DAGTask, approval *core.ApprovalConfig) DAGTask {
	for {
		task = m.executeDAGTask(ctx, id, config, base, task, deps, approval)
		if task.Status != "failed" || task.FailurePhase != "execution" || task.AutoRetriesUsed >= config.MaxRetries || !canAutomaticallyRetry(ctx, task.Error) {
			return task
		}
		task.AutoRetriesUsed++
		next := time.Now().UTC().Add(retryDelay(config, task.AutoRetriesUsed))
		task.NextRetryAt = &next
		task.Status = "retrying"
		task.ResumeContext = truncateWorkerText(task.Error+"\n"+task.Output, 4096)
		m.publishTask(id, task)
		m.retryEvent(id, task.ID, task.Worker.ProfileID, task.AutoRetriesUsed, config.MaxRetries, next, task.Error)
		if err := waitForRetry(ctx, time.Until(next)); err != nil {
			task.Status = "cancelled"
			task.Error = err.Error()
			task.NextRetryAt = nil
			return task
		}
		task.Error = ""
		task.NextRetryAt = nil
	}
}

func taskFailureSummary(tasks []DAGTask) string {
	failed, blocked, completed := 0, 0, 0
	first := ""
	for _, task := range tasks {
		switch task.Status {
		case "completed":
			completed++
		case "blocked":
			blocked++
		default:
			failed++
			if first == "" {
				first = task.ID + ": " + truncateWorkerText(task.Error, 700)
			}
		}
	}
	return fmt.Sprintf("%d task(s) failed · %d blocked by dependencies · %d completed. %s", failed, blocked, completed, first)
}

func (m *Manager) rootSession(id string, tier Tier, p Profile) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[id]; run != nil {
		for i := len(run.Attempts) - 1; i >= 0; i-- {
			a := run.Attempts[i]
			if a.TaskID == "" && a.ParentID == "" && a.Tier == tier {
				if a.ProfileHash == profileHash(p) {
					return a.SessionID
				}
				return ""
			}
		}
	}
	return ""
}
func (m *Manager) pauseAssignmentRetry(ctx context.Context, id string, config Config, used int, tier Tier, cause string) error {
	next := time.Now().UTC().Add(retryDelay(config, used))
	m.mu.Lock()
	if run := m.runs[id]; run != nil {
		run.AutoRetriesUsed = used
		run.NextRetryAt = &next
		if err := m.saveRunLocked(run); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	m.mu.Unlock()
	m.retryEvent(id, "", tier, used, config.MaxRetries, next, cause)
	err := waitForRetry(ctx, time.Until(next))
	m.mu.Lock()
	if run := m.runs[id]; run != nil {
		run.NextRetryAt = nil
	}
	m.mu.Unlock()
	return err
}

// The same bounded policy covers hierarchical assignments and read-only plans.
func (m *Manager) runAssignmentWithRetries(ctx context.Context, id string, config Config, tier Tier, input string, runner Runner) (string, error) {
	used := 0
	originalInput := input
	var result string
	var err error
	for {
		result, err = runner.RunFromTier(ctx, config, tier, input)
		if err == nil || used >= config.MaxRetries || !canAutomaticallyRetry(ctx, err.Error()) {
			return result, err
		}
		used++
		if pauseErr := m.pauseAssignmentRetry(ctx, id, config, used, tier, err.Error()); pauseErr != nil {
			return result, pauseErr
		}
		p, _ := config.Profile(tier)
		runner.SessionID = m.rootSession(id, tier, p)
		observation := "Continue after a failed attempt. Existing work is retained; inspect actual state before repeating effects. Previous failure:\n" + err.Error() + "\n" + result
		input = workerInputWithEvidence(originalInput, []string{observation}, p.MaxInputBytes-len(workerInstructions(config, tier, p))-128)
	}
}

func (m *Manager) planWithRetries(ctx context.Context, id string, config Config, objective string, runner Runner) ([]DAGTask, error) {
	for used := 0; ; used++ {
		tasks, err := runner.Plan(ctx, config, objective)
		if err == nil || used >= config.MaxRetries || !canAutomaticallyRetry(ctx, err.Error()) {
			return tasks, err
		}
		if pauseErr := m.pauseAssignmentRetry(ctx, id, config, used+1, Primary, err.Error()); pauseErr != nil {
			return nil, pauseErr
		}
		p, _ := config.Profile(Primary)
		p.FullAccess, p.AllowEdits, p.AllowCommands = false, false, false
		runner.SessionID = m.rootSession(id, Primary, p)
	}
}
