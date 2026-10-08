package orchestrator

import (
	"errors"
)

// RetryDAGTask queues a new attempt without discarding the previous worktree.
// ResetDAGTask remains the explicit destructive alternative.
func (m *Manager) RetryDAGTask(id, taskID string) error {
	return m.retryDAGTask(id, taskID, false)
}

// ResolveDAGTaskConflicts queues an explicit, non-destructive worker resolution.
func (m *Manager) ResolveDAGTaskConflicts(id, taskID string) error {
	return m.retryDAGTask(id, taskID, true)
}

func (m *Manager) retryDAGTask(id, taskID string, resolve bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if m.closed || run == nil || run.DAG == nil || run.cancel != nil {
		return errors.New("stop execution before continuing a task")
	}
	for _, other := range run.DAG.Tasks {
		if other.Status == "completed" {
			for _, dep := range dependencyOrder(run.DAG.Tasks, other.ID) {
				if dep.ID == taskID {
					return errors.New("a completed task depends on this result; review its changes first")
				}
			}
		}
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.ID != taskID {
			continue
		}
		if task.Status != "failed" && task.Status != "cancelled" && task.Status != "blocked" {
			return errors.New("only unsuccessful tasks can be continued")
		}
		if task.Worktree != nil {
			if err := (WorktreeManager{Workspace: run.Workspace, RunID: id}).verify(m.ctx, task.Worktree); err != nil {
				return err
			}
		}
		previous := cloneDAG(run.DAG)
		previousError := run.Error
		if resolve {
			if task.Worktree == nil || task.FailurePhase != "dependencies" {
				return errors.New("this task has no failed dependency integration")
			}
			config := Config{Workspace: run.Workspace, Profiles: run.Profiles, AccessMode: run.AccessMode, MaxRetries: run.MaxRetries, RetryDelaySeconds: run.RetryDelaySeconds}
			profile, _, err := resolveTaskProfile(config, task.requestedWorker())
			if err != nil {
				return err
			}
			if !profile.AllowEdits {
				return errors.New("enable file edits or full access in run settings before resolving conflicts")
			}
		}
		task.ResolveConflicts = resolve
		task.ResumeContext = truncateWorkerText(task.Error+"\n"+task.Output, 4096)
		task.Status = "pending"
		task.Error = ""
		run.Error = ""
		for j := range run.DAG.Tasks {
			blocked := &run.DAG.Tasks[j]
			if blocked.Status != "blocked" {
				continue
			}
			for _, dep := range dependencyOrder(run.DAG.Tasks, blocked.ID) {
				if dep.ID == taskID {
					blocked.Status = "pending"
					blocked.Error = ""
					break
				}
			}
		}
		if err := m.saveDAGLocked(run); err != nil {
			run.DAG = previous
			run.Error = previousError
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

func (m *Manager) taskSession(id, taskID string, p Profile) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.runs[id]; run != nil {
		for i := len(run.Attempts) - 1; i >= 0; i-- {
			a := run.Attempts[i]
			if a.TaskID == taskID {
				if a.ProfileHash == profileHash(p) {
					return a.SessionID
				}
				return ""
			}
		}
	}
	return ""
}

type TaskPreview struct {
	Path      string   `json:"path"`
	Diff      string   `json:"diff"`
	Status    string   `json:"status"`
	Truncated bool     `json:"truncated"`
	Conflicts []string `json:"conflicts,omitempty"`
}

func (m *Manager) TaskPreview(id, taskID string) (TaskPreview, error) {
	m.mu.Lock()
	run := m.runs[id]
	if run == nil || run.DAG == nil {
		m.mu.Unlock()
		return TaskPreview{}, errors.New("task graph not found")
	}
	var tree *Worktree
	for _, task := range run.DAG.Tasks {
		if task.ID == taskID && task.Worktree != nil {
			copy := *task.Worktree
			tree = &copy
			break
		}
	}
	workspace := run.Workspace
	m.mu.Unlock()
	w := WorktreeManager{Workspace: workspace, RunID: id}
	if err := w.verify(m.ctx, tree); err != nil {
		return TaskPreview{}, err
	}
	preview := TaskPreview{Path: tree.Path}
	diff, err := gitCommand(m.ctx, tree.Path, "diff", "--no-ext-diff", "--no-textconv", tree.BaseRef, "--")
	if err != nil {
		return preview, err
	}
	preview.Truncated = len(diff) > 256<<10
	preview.Diff = truncateWorkerText(diff, 256<<10)
	preview.Status, err = gitCommand(m.ctx, tree.Path, "status", "--short")
	if err == nil {
		preview.Conflicts, err = conflictFiles(m.ctx, tree.Path)
	}
	return preview, err
}
