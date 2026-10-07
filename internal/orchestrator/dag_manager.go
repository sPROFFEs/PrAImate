package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func cloneDAG(d *DAG) *DAG {
	if d == nil {
		return nil
	}
	raw, _ := json.Marshal(d)
	var copy DAG
	_ = json.Unmarshal(raw, &copy)
	return &copy
}
func cloneTask(task DAGTask) DAGTask { d := cloneDAG(&DAG{Tasks: []DAGTask{task}}); return d.Tasks[0] }

func (m *Manager) saveDAGLocked(run *Run) error {
	return m.saveRunLocked(run)
}

// PlanDAG creates a persistent draft. Executing it is a separate user action.
func (m *Manager) PlanDAG(objective string, config Config, parallel int) (string, error) {
	if m.core == nil {
		return "", errors.New("database is unavailable")
	}
	if m.loadErr != nil {
		return "", m.loadErr
	}
	if err := config.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(objective) == "" || len(objective) > 16<<10 {
		return "", errors.New("objective must contain 1–16384 bytes")
	}
	if parallel == 0 {
		parallel = 2
	}
	if parallel < 1 || parallel > 4 {
		return "", errors.New("parallelism must be between 1 and 4")
	}
	id, err := newRunID()
	if err != nil {
		return "", err
	}
	worktrees := WorktreeManager{Workspace: config.Workspace, RunID: id}
	base, branch, err := worktrees.Repository(m.ctx)
	if err != nil {
		return "", err
	}
	if err = SaveConfig(m.ctx, m.core, config); err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return "", errors.New("worker manager is shutting down")
	}
	if err = m.checkDAGCapacityLocked(); err != nil {
		m.mu.Unlock()
		cancel()
		return "", err
	}
	title := truncateWorkerText(strings.TrimSpace(objective), 80)
	if _, err = m.core.CreateChat(m.ctx, core.CreateChatRequest{ID: id, Title: title, CLIAgent: "workers", WorkspacePath: config.Workspace, Settings: core.ChatSettings{Surface: "workers"}}); err != nil {
		m.mu.Unlock()
		cancel()
		return "", err
	}
	configJSON, _ := json.Marshal(config)
	_, err = m.core.AddMessage(m.ctx, id, "system", string(configJSON), nil)
	now := time.Now().UTC()
	run := &Run{AccessMode: config.AccessMode, MaxRetries: config.MaxRetries, RetryDelaySeconds: config.RetryDelaySeconds, ID: id, Title: title, Task: objective, CurrentTask: objective, Workspace: config.Workspace, Status: "planning", Profiles: append([]Profile(nil), config.Profiles...), StartedAt: now, UpdatedAt: now, Events: []Event{}, Turns: []Turn{}, DAG: &DAG{MaxParallel: parallel, BaseCommit: base, TargetBranch: branch}, config: config, cancel: cancel}
	m.runs[id] = run
	if err == nil {
		err = m.saveDAGLocked(run)
	}
	if err != nil {
		delete(m.runs, id)
		_ = m.core.DeleteChat(context.Background(), id)
		m.mu.Unlock()
		cancel()
		return "", err
	}
	m.mu.Unlock()
	m.plan(ctx, cancel, id, config, objective)
	return id, nil
}

func (m *Manager) plan(ctx context.Context, cancel context.CancelFunc, id string, config Config, objective string) {
	go func() {
		defer cancel()
		runner := Runner{Resolve: ResolveRuntimeWithCore(m.core), Emit: func(event Event) { m.recordDAGEvent(id, event) }}
		tasks, planErr := m.planWithRetries(ctx, id, config, objective, runner)
		m.mu.Lock()
		defer m.mu.Unlock()
		run := m.runs[id]
		if run == nil {
			return
		}
		run.cancel = nil
		if planErr != nil {
			run.Status = "failed"
			run.Error = planErr.Error()
		} else {
			run.Status = "draft"
			run.DAG.Tasks = tasks
			run.Error = ""
		}
		if ctx.Err() != nil {
			run.Status = "cancelled"
		}
		_ = m.saveDAGLocked(run)
	}()
}

func (m *Manager) checkDAGCapacityLocked() error {
	active := 0
	for _, run := range m.runs {
		if run.cancel != nil {
			active++
		}
	}
	if active >= 3 {
		return errors.New("at most three worker chats may run concurrently")
	}
	return nil
}

func (m *Manager) recordDAGEvent(id string, event Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return
	}
	recordWorkerActivity(run, event)
	if checkpointActivity(run, event) {
		if err := m.saveDAGLocked(run); err != nil && run.cancel != nil {
			run.cancel()
		}
	}
}

// UpdateDAG accepts only editable plan fields, never model-supplied result state.
func (m *Manager) UpdateDAG(id string, tasks []DAGTask, parallel int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.DAG == nil {
		return errors.New("task graph not found")
	}
	if run.cancel != nil || run.Status != "draft" {
		return errors.New("only a draft plan can be edited")
	}
	if parallel < 1 || parallel > 4 {
		return errors.New("parallelism must be between 1 and 4")
	}
	clean := make([]DAGTask, 0, len(tasks))
	for _, t := range tasks {
		request := t.Worker
		clean = append(clean, DAGTask{ID: t.ID, Description: t.Description, Dependencies: append([]string(nil), t.Dependencies...), Worker: request, RequestedWorker: &request, Status: "pending"})
	}
	if err := ValidateDAG(clean, run.config); err != nil {
		return err
	}
	previous := run.DAG
	run.DAG = cloneDAG(previous)
	run.DAG.Tasks = clean
	run.DAG.MaxParallel = parallel
	if err := m.saveDAGLocked(run); err != nil {
		run.DAG = previous
		return err
	}
	return nil
}

func (m *Manager) ExecuteDAG(id string, approvalProvider func(string) *core.ApprovalConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if m.closed {
		return errors.New("worker manager is shutting down")
	}
	if run == nil || run.DAG == nil {
		return errors.New("task graph not found")
	}
	if run.cancel != nil {
		return errors.New("task graph is already active")
	}
	if err := m.checkDAGCapacityLocked(); err != nil {
		return err
	}
	if err := ValidateDAG(run.DAG.Tasks, run.config); err != nil {
		return err
	}
	pending := false
	for _, task := range run.DAG.Tasks {
		if task.Status == "pending" {
			pending = true
		}
	}
	if !pending {
		return errors.New("no pending tasks; continue or reset an unsuccessful task")
	}
	worktrees := WorktreeManager{Workspace: run.Workspace, RunID: run.ID}
	base, branch, err := worktrees.Repository(m.ctx)
	if err != nil {
		return err
	}
	if branch != run.DAG.TargetBranch || (base != run.DAG.BaseCommit && base != run.DAG.LastMergedCommit) {
		return errors.New("workspace HEAD changed after planning; create a new plan from the current branch")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	run.cancel = cancel
	run.Status = "running"
	run.Error = ""
	if err = m.saveDAGLocked(run); err != nil {
		run.cancel = nil
		cancel()
		return err
	}
	graph := cloneDAG(run.DAG)
	config := run.config
	go m.executeDAG(ctx, cancel, id, config, graph, approvalProvider)
	return nil
}

func dependencyOrder(tasks []DAGTask, id string) []DAGTask {
	byID := map[string]DAGTask{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	seen := map[string]bool{}
	out := []DAGTask{}
	var visit func(string)
	visit = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		for _, dep := range byID[key].Dependencies {
			visit(dep)
		}
		out = append(out, byID[key])
	}
	for _, dep := range byID[id].Dependencies {
		visit(dep)
	}
	return out
}

func (m *Manager) publishTask(id string, task DAGTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.DAG == nil {
		return
	}
	for i := range run.DAG.Tasks {
		if run.DAG.Tasks[i].ID == task.ID {
			run.DAG.Tasks[i] = cloneTask(task)
			_ = m.saveDAGLocked(run)
			return
		}
	}
}

func (m *Manager) executeDAG(ctx context.Context, cancel context.CancelFunc, id string, config Config, graph *DAG, approvalProvider func(string) *core.ApprovalConfig) {
	defer cancel()
	var approval *core.ApprovalConfig
	if approvalProvider != nil {
		approval = approvalProvider(id)
	}
	done := make(chan DAGTask, graph.MaxParallel)
	active := 0
	for {
		launched := false
		for i := range graph.Tasks {
			task := &graph.Tasks[i]
			if task.Status != "pending" {
				continue
			}
			deps := dependencyOrder(graph.Tasks, task.ID)
			ready := true
			blocked := false
			for _, dep := range deps {
				if dep.Status != "completed" {
					ready = false
				}
				if dep.Status == "failed" || dep.Status == "blocked" || dep.Status == "cancelled" {
					blocked = true
				}
			}
			if blocked {
				task.Status = "blocked"
				task.Error = "A dependency failed. Inspect its worktree before retrying."
				m.publishTask(id, *task)
				continue
			}
			if active >= graph.MaxParallel || !ready || ctx.Err() != nil {
				continue
			}
			task.Status = "running"
			m.publishTask(id, *task)
			active++
			launched = true
			copy := cloneTask(*task)
			go func() { done <- m.executeDAGTaskWithRetries(ctx, id, config, graph.BaseCommit, copy, deps, approval) }()
		}
		if active == 0 {
			if !launched {
				break
			}
		}
		if active > 0 {
			result := <-done
			active--
			for i := range graph.Tasks {
				if graph.Tasks[i].ID == result.ID {
					graph.Tasks[i] = result
					m.publishTask(id, result)
					break
				}
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return
	}
	run.cancel = nil
	run.Status = "review"
	run.Result = "Review each task's Git diff, then accept or reject its result."
	for _, task := range graph.Tasks {
		if task.Status != "completed" {
			run.Status = "failed"
			run.Error = taskFailureSummary(graph.Tasks)
		}
	}
	if ctx.Err() != nil {
		run.Status = "cancelled"
		run.Error = "Execution cancelled; inspect worktrees before retrying interrupted tasks."
	}
	_ = m.saveDAGLocked(run)
}

func (m *Manager) executeDAGTask(ctx context.Context, id string, config Config, base string, task DAGTask, deps []DAGTask, approval *core.ApprovalConfig) DAGTask {
	phase := "setup"
	task.NextRetryAt = nil
	fail := func(err error) DAGTask {
		task.FailurePhase = phase
		task.Status = "failed"
		task.Error = err.Error()
		if ctx.Err() != nil {
			task.Status = "cancelled"
		}
		return task
	}
	request := task.requestedWorker()
	task.RequestedWorker = &request
	p, resolved, err := resolveTaskProfile(config, request)
	if err != nil {
		return fail(err)
	}
	task.Worker = resolved
	worktrees := WorktreeManager{Workspace: config.Workspace, RunID: id}
	if task.Worktree == nil {
		task.Worktree, err = worktrees.Create(ctx, task.ID, base)
	} else {
		err = worktrees.verify(ctx, task.Worktree)
	}
	if err != nil {
		return fail(err)
	}
	m.publishTask(id, task)
	commits := []string{}
	var evidence strings.Builder
	for _, dep := range deps {
		if dep.Result == nil {
			return fail(errors.New("dependency has no saved result"))
		}
		if dep.Result.ResultCommit != dep.Result.BaseCommit {
			commits = append(commits, dep.Result.ResultCommit)
		}
		evidence.WriteString(dep.ID + ": " + truncateWorkerText(dep.Output, 512) + "\n")
	}
	// Old snapshots updated BaseRef only after integrating every dependency.
	if task.ResumeContext != "" && task.Worktree.BaseRef != base && len(task.Worktree.IntegratedCommits) == 0 {
		task.DependenciesReady = true
	}
	phase = "dependencies"
	if !task.DependenciesReady {
		if err = worktrees.IntegrateDependencies(ctx, task.Worktree, commits); err != nil {
			return fail(err)
		}
		task.DependenciesReady = true
	}
	m.publishTask(id, task)
	local := config
	local.Workspace = task.Worktree.Path
	local.Profiles = append([]Profile(nil), config.Profiles...)
	for i := range local.Profiles {
		if local.Profiles[i].Tier == p.Tier {
			local.Profiles[i] = p
		}
	}
	runner := Runner{Resolve: ResolveRuntimeWithCore(m.core), Approval: approval, NoDelegation: true, Emit: func(event Event) { event.TaskID = task.ID; m.recordDAGEvent(id, event) }}
	if task.ResumeContext != "" {
		runner.SessionID = m.taskSession(id, task.ID, p)
	}
	input := "Task " + task.ID + " (isolated worktree):\n" + task.Description
	var observations []string
	if len(deps) > 0 {
		observations = append(observations, "Dependency results; source changes are already present:\n"+truncateWorkerText(evidence.String(), 4096))
	}
	if task.ResumeContext != "" {
		observations = append(observations, "Continue the assignment with existing changes. Inspect files and outcomes before repeating effects. Previous outcome:\n"+task.ResumeContext)
	}
	// Reserve space for the isolated-task instructions and turn counter.
	input = workerInputWithEvidence(input, observations, p.MaxInputBytes-len(workerInstructions(local, p.Tier, p))-512)
	phase = "execution"
	task.Output, err = runner.RunFromTier(ctx, local, p.Tier, input)
	if err != nil {
		return fail(err)
	}
	phase = "checkpoint"
	task.Result, err = worktrees.Complete(ctx, task.Worktree, resolved)
	if err != nil {
		return fail(err)
	}
	task.FailurePhase = ""
	task.Status = "completed"
	task.Review = "pending"
	task.Error = ""
	return task
}

func (m *Manager) ReviewDAGTask(id, taskID, decision string) error {
	if decision != "accepted" && decision != "rejected" {
		return errors.New("review decision must be accepted or rejected")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.DAG == nil || run.cancel != nil {
		return errors.New("stop execution before reviewing results")
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.ID != taskID {
			continue
		}
		if task.Status != "completed" || task.Result == nil || task.Review == "merged" {
			return errors.New("task has no unmerged result")
		}
		if decision == "accepted" {
			for _, dep := range dependencyOrder(run.DAG.Tasks, taskID) {
				if dep.Review != "accepted" && dep.Review != "merged" {
					return errors.New("accept this task's dependencies first")
				}
			}
		}
		if decision == "rejected" {
			for _, other := range run.DAG.Tasks {
				if other.Review == "accepted" || other.Review == "merged" {
					for _, dep := range dependencyOrder(run.DAG.Tasks, other.ID) {
						if dep.ID == taskID {
							return errors.New("reject dependent results first")
						}
					}
				}
			}
		}
		old := task.Review
		oldStatus, oldResult := run.Status, run.Result
		task.Review = decision
		updateDAGReviewStatus(run)
		if err := m.saveDAGLocked(run); err != nil {
			task.Review = old
			run.Status, run.Result = oldStatus, oldResult
			return err
		}
		return nil
	}
	return errors.New("task not found")
}

func (m *Manager) MergeDAG(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if m.closed {
		return errors.New("worker manager is shutting down")
	}
	if run == nil || run.DAG == nil || run.cancel != nil {
		return errors.New("stop execution before merging results")
	}
	if run.DAG.MergeWorktree != nil {
		return errors.New("inspect and clean the previous merge worktree before retrying")
	}
	selected := map[string]bool{}
	ordered := []DAGTask{}
	var add func(DAGTask) error
	add = func(task DAGTask) error {
		if selected[task.ID] || task.Review == "merged" {
			return nil
		}
		if task.Review != "accepted" || task.Result == nil {
			return errors.New("accept all dependencies before merging")
		}
		for _, dep := range dependencyOrder(run.DAG.Tasks, task.ID) {
			if err := add(dep); err != nil {
				return err
			}
		}
		selected[task.ID] = true
		ordered = append(ordered, task)
		return nil
	}
	for _, task := range run.DAG.Tasks {
		if task.Review == "accepted" {
			if err := add(task); err != nil {
				return err
			}
		}
	}
	if len(ordered) == 0 {
		return errors.New("there are no accepted results to merge")
	}
	commits := []string{}
	for _, task := range ordered {
		if task.Result.BaseCommit != task.Result.ResultCommit {
			commits = append(commits, task.Result.ResultCommit)
		}
	}
	worktrees := WorktreeManager{Workspace: run.Workspace, RunID: id}
	_, branch, err := worktrees.Repository(m.ctx)
	if err != nil {
		return err
	}
	if branch != run.DAG.TargetBranch {
		return errors.New("return to the run's target branch before merging")
	}
	ctx, cancel := context.WithCancel(m.ctx)
	run.cancel = cancel
	run.Status = "merging"
	run.Error = ""
	run.DAG.MergeCommit = ""
	run.DAG.MergeTasks = nil
	for _, task := range ordered {
		run.DAG.MergeTasks = append(run.DAG.MergeTasks, task.ID)
	}
	if err := m.saveDAGLocked(run); err != nil {
		run.cancel = nil
		cancel()
		return err
	}
	target := run.DAG.TargetBranch
	go m.mergeDAG(ctx, cancel, id, worktrees, target, commits, selected)
	return nil
}

func (m *Manager) mergeDAG(ctx context.Context, cancel context.CancelFunc, id string, worktrees WorktreeManager, target string, commits []string, selected map[string]bool) {
	defer cancel()
	checkpoint := func(tree *Worktree, ready bool) error {
		m.mu.Lock()
		defer m.mu.Unlock()
		run := m.runs[id]
		if run == nil {
			return errors.New("worker run disappeared")
		}
		copy := *tree
		run.DAG.MergeWorktree = &copy
		if ready {
			run.DAG.MergeCommit = tree.BaseRef
		}
		return m.saveDAGLocked(run)
	}
	tree, mergeErr := worktrees.Merge(ctx, target, commits, checkpoint)
	m.mu.Lock()
	run := m.runs[id]
	if run == nil {
		m.mu.Unlock()
		return
	}
	if tree != nil {
		copy := *tree
		run.DAG.MergeWorktree = &copy
	}
	// Cancellation can arrive just after Git changed the branch. Check the
	// recorded candidate before reporting an unknown/failed merge.
	if mergeErr != nil && run.DAG.MergeCommit != "" && mergedCandidate(run.Workspace, run.DAG) {
		mergeErr = nil
	}
	if mergeErr != nil {
		run.cancel = nil
		run.Status = "review"
		run.Error = mergeErr.Error()
		_ = m.saveDAGLocked(run)
		m.mu.Unlock()
		return
	}
	for i := range run.DAG.Tasks {
		if selected[run.DAG.Tasks[i].ID] {
			run.DAG.Tasks[i].Review = "merged"
		}
	}
	run.DAG.LastMergedCommit = run.DAG.MergeCommit
	if err := m.saveDAGLocked(run); err != nil {
		run.cancel = nil
		run.Status = "review"
		m.mu.Unlock()
		return
	}
	trees := []*Worktree{tree}
	for _, task := range run.DAG.Tasks {
		if selected[task.ID] && task.Worktree != nil {
			copy := *task.Worktree
			trees = append(trees, &copy)
		}
	}
	m.mu.Unlock()
	var cleanupErr error
	for _, owned := range trees {
		if owned == nil {
			continue
		}
		if cleanupErr = worktrees.Remove(ctx, owned); cleanupErr != nil {
			break
		}
		m.mu.Lock()
		run = m.runs[id]
		if owned.ID == "review-merge" {
			run.DAG.MergeWorktree = nil
		} else {
			for i := range run.DAG.Tasks {
				if run.DAG.Tasks[i].ID == owned.TaskID {
					run.DAG.Tasks[i].Worktree = nil
				}
			}
		}
		_ = m.saveDAGLocked(run)
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run = m.runs[id]
	run.cancel = nil
	run.Status = "review"
	updateDAGReviewStatus(run)
	if cleanupErr != nil {
		run.Error = "Merged successfully; cleanup pending: " + cleanupErr.Error()
	}
	_ = m.saveDAGLocked(run)
}

func mergedCandidate(workspace string, graph *DAG) bool {
	if graph.MergeCommit == "" || graph.TargetBranch == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := gitCommand(ctx, workspace, "merge-base", "--is-ancestor", graph.MergeCommit, "refs/heads/"+graph.TargetBranch)
	return err == nil
}

// ResetDAGTask explicitly discards failed work. Completed descendants must be
// reviewed first; their saved commits are never silently invalidated.
func (m *Manager) ResetDAGTask(id, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.DAG == nil || run.cancel != nil {
		return errors.New("stop execution before resetting a task")
	}
	for _, other := range run.DAG.Tasks {
		if other.Status == "completed" {
			for _, dep := range dependencyOrder(run.DAG.Tasks, other.ID) {
				if dep.ID == taskID {
					return errors.New("cannot reset a dependency of a completed result")
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
			return errors.New("only unsuccessful tasks can be reset")
		}
		w := WorktreeManager{Workspace: run.Workspace, RunID: id}
		if err := w.RemoveResultRef(m.ctx, task.ID); err != nil {
			return err
		}
		if task.Worktree != nil {
			if err := w.Remove(m.ctx, task.Worktree); err != nil {
				return err
			}
		}
		task.Status = "pending"
		task.Error = ""
		task.Output = ""
		task.ResumeContext = ""
		task.DependenciesReady = false
		task.Worktree = nil
		task.Result = nil
		run.Error = ""
		for j := range run.DAG.Tasks {
			if run.DAG.Tasks[j].Status == "blocked" {
				run.DAG.Tasks[j].Status = "pending"
				run.DAG.Tasks[j].Error = ""
			}
		}
		return m.saveDAGLocked(run)
	}
	return errors.New("task not found")
}

func updateDAGReviewStatus(run *Run) {
	for _, task := range run.DAG.Tasks {
		if task.Status != "completed" || (task.Review != "merged" && task.Review != "rejected") {
			if run.Status == "completed" {
				run.Status = "review"
			}
			return
		}
	}
	run.Status = "completed"
	run.Result = "Task review is complete. Accepted changes were merged; rejected results remain in the saved history."
}

func (m *Manager) cleanupDAGLocked(run *Run) error {
	w := WorktreeManager{Workspace: run.Workspace, RunID: run.ID}
	if run.DAG.MergeWorktree != nil {
		if err := w.Remove(m.ctx, run.DAG.MergeWorktree); err != nil {
			return err
		}
		run.DAG.MergeWorktree = nil
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.Worktree == nil {
			continue
		}
		if err := w.Remove(m.ctx, task.Worktree); err != nil {
			return err
		}
		task.Worktree = nil
	}
	return nil
}

func (m *Manager) CleanupDAG(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil || run.DAG == nil || run.cancel != nil {
		return errors.New("stop execution before cleaning worktrees")
	}
	// Keep unreviewed/completed and failed task trees until explicit deletion or reset.
	w := WorktreeManager{Workspace: run.Workspace, RunID: id}
	if run.DAG.MergeWorktree != nil {
		if err := w.Remove(m.ctx, run.DAG.MergeWorktree); err != nil {
			return err
		}
		run.DAG.MergeWorktree = nil
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.Worktree != nil && (task.Review == "merged" || task.Review == "rejected") {
			if err := w.Remove(m.ctx, task.Worktree); err != nil {
				return err
			}
			task.Worktree = nil
		}
	}
	return m.saveDAGLocked(run)
}

func newRunID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "workers-" + hex.EncodeToString(random[:]), nil
}
