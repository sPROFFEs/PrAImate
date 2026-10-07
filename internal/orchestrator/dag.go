package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

// WorkerConfig selects an existing profile and optionally overrides its route.
// Provider is resolved from the existing model ID/endpoint, never a new registry.
type WorkerConfig struct {
	ProfileID Tier   `json:"profile"`
	Runtime   string `json:"runtime,omitempty"`
	CLI       string `json:"cli,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
}

type DAGTask struct {
	AutoRetriesUsed int          `json:"autoRetriesUsed,omitempty"`
	NextRetryAt     *time.Time   `json:"nextRetryAt,omitempty"`
	FailurePhase    string       `json:"failurePhase,omitempty"`
	ID              string       `json:"id"`
	Description     string       `json:"description"`
	Dependencies    []string     `json:"dependencies"`
	Worker          WorkerConfig `json:"worker"`
	// RequestedWorker retains profile inheritance and explicit overrides. Worker
	// records the last resolved route, which must never become a retry override.
	RequestedWorker   *WorkerConfig `json:"requestedWorker,omitempty"`
	Status            string        `json:"status"`
	Review            string        `json:"review,omitempty"`
	Error             string        `json:"error,omitempty"`
	Output            string        `json:"output,omitempty"`
	Worktree          *Worktree     `json:"worktree,omitempty"`
	Result            *TaskResult   `json:"result,omitempty"`
	ResumeContext     string        `json:"resumeContext,omitempty"`
	DependenciesReady bool          `json:"dependenciesReady,omitempty"`
}

type TaskResult struct {
	TaskID        string       `json:"taskID"`
	WorkerConfig  WorkerConfig `json:"worker"`
	BaseCommit    string       `json:"baseCommit"`
	ResultCommit  string       `json:"resultCommit"`
	ResultRef     string       `json:"resultRef,omitempty"`
	Diff          string       `json:"diff"`
	DiffTruncated bool         `json:"diffTruncated,omitempty"`
	ChangedFiles  []string     `json:"changedFiles"`
}

type DAG struct {
	Tasks            []DAGTask `json:"tasks"`
	MaxParallel      int       `json:"maxParallel"`
	BaseCommit       string    `json:"baseCommit"`
	TargetBranch     string    `json:"targetBranch"`
	MergeWorktree    *Worktree `json:"mergeWorktree,omitempty"`
	MergeCommit      string    `json:"mergeCommit,omitempty"`
	LastMergedCommit string    `json:"lastMergedCommit,omitempty"`
	MergeTasks       []string  `json:"mergeTasks,omitempty"`
}

var taskIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func (task DAGTask) requestedWorker() WorkerConfig {
	if task.RequestedWorker != nil {
		return *task.RequestedWorker
	}
	return task.Worker
}

// restoreTaskRequests upgrades snapshots written before requested routes were
// stored separately. Those executors populated Provider when resolving a route.
// Original profile values are inherited; differing values remain task overrides.
// Legacy snapshots cannot distinguish an explicit pin equal to that profile.
func restoreTaskRequests(run *Run, original Config) {
	if run.DAG == nil {
		return
	}
	attemptedIDs := make(map[string]bool)
	for _, event := range run.Events {
		if event.TaskID != "" {
			attemptedIDs[event.TaskID] = true
		}
	}
	for i := range run.DAG.Tasks {
		task := &run.DAG.Tasks[i]
		if task.RequestedWorker != nil {
			continue
		}
		request := task.Worker
		tier := request.ProfileID
		if tier == "" {
			tier = Middle
		}
		attempted := attemptedIDs[task.ID] || task.Worktree != nil || task.Result != nil || task.Status == "running" || task.Status == "completed" || task.Status == "failed" || task.Status == "cancelled"
		if profile, ok := original.Profile(tier); ok && attempted && request.Provider != "" && request.Runtime != "" {
			if request.Runtime == profile.Runtime {
				request.Runtime = ""
			}
			if request.CLI == profile.CLI {
				request.CLI = ""
			}
			if request.Model == profile.Model {
				request.Model = ""
			}
			if request.Endpoint == profile.Endpoint {
				request.Endpoint = ""
			}
		}
		request.Provider = ""
		task.RequestedWorker = &request
	}
}

func resolveTaskProfile(config Config, worker WorkerConfig) (Profile, WorkerConfig, error) {
	if worker.ProfileID == "" {
		worker.ProfileID = Middle
	}
	p, ok := config.Profile(worker.ProfileID)
	if !ok {
		return p, worker, errors.New("unknown worker profile")
	}
	if worker.Runtime == "" && worker.CLI != "" {
		worker.Runtime = "cli"
	}
	if worker.Runtime == "" && worker.Endpoint != "" {
		worker.Runtime = "native"
	}
	if worker.Runtime != "" && worker.Runtime != p.Runtime {
		p.Runtime = worker.Runtime
		if p.Runtime == "native" {
			p.CLI = ""
			p.MaxOutputTokens = 2048
		} else {
			p.Endpoint = ""
			p.MaxOutputTokens = 0
		}
	}
	if worker.CLI != "" {
		p.CLI = worker.CLI
	}
	if worker.Model != "" {
		p.Model = worker.Model
	}
	if worker.Endpoint != "" {
		p.Endpoint = worker.Endpoint
	}
	if p.Runtime != "cli" || p.CLI != "codex" {
		p.ReasoningEffort = ""
	}
	clone := config
	clone.Profiles = append([]Profile(nil), config.Profiles...)
	for i := range clone.Profiles {
		if clone.Profiles[i].Tier == p.Tier {
			clone.Profiles[i] = p
		}
	}
	if err := clone.Validate(); err != nil {
		return p, worker, err
	}
	worker.Runtime = p.Runtime
	worker.CLI = p.CLI
	worker.Model = p.Model
	worker.Endpoint = p.Endpoint
	worker.Provider = p.Endpoint
	if p.Runtime == "cli" {
		worker.Provider = "configured by " + p.CLI
		if prefix, _, ok := strings.Cut(p.Model, "/"); ok {
			worker.Provider = prefix
		}
	}
	return p, worker, nil
}

func ValidateDAG(tasks []DAGTask, config Config) error {
	if len(tasks) < 1 || len(tasks) > 32 {
		return errors.New("a task graph must contain 1–32 tasks")
	}
	byID := map[string]DAGTask{}
	total := 0
	for _, task := range tasks {
		if !taskIDPattern.MatchString(task.ID) || task.ID == "review-merge" || byID[task.ID].ID != "" {
			return fmt.Errorf("invalid or duplicate task ID %q", task.ID)
		}
		if strings.TrimSpace(task.Description) == "" || len(task.Description) > 8192 {
			return fmt.Errorf("task %s requires a description of 1–8192 bytes", task.ID)
		}
		total += len(task.Description)
		if total > 48<<10 {
			return errors.New("task descriptions exceed 48 KiB")
		}
		if _, _, err := resolveTaskProfile(config, task.requestedWorker()); err != nil {
			return fmt.Errorf("task %s: %w", task.ID, err)
		}
		byID[task.ID] = task
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return errors.New("task graph contains a dependency cycle")
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		seen := map[string]bool{}
		for _, dep := range byID[id].Dependencies {
			if byID[dep].ID == "" || seen[dep] {
				return fmt.Errorf("task %s has an unknown or duplicate dependency %q", id, dep)
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for _, task := range tasks {
		if err := visit(task.ID); err != nil {
			return err
		}
	}
	return nil
}

func parsePlan(text string, config Config) ([]DAGTask, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json\n") || strings.HasPrefix(text, "```\n") {
		_, text, _ = strings.Cut(text, "\n")
		text = strings.TrimSuffix(text, "\n```")
	}
	if len(text) > 64<<10 {
		return nil, errors.New("coordinator plan exceeds 64 KiB")
	}
	var plan struct {
		Tasks []struct {
			ID           string       `json:"id"`
			Description  string       `json:"description"`
			Dependencies []string     `json:"dependencies"`
			Worker       WorkerConfig `json:"worker"`
		} `json:"tasks"`
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return nil, fmt.Errorf("coordinator must return a task graph JSON object: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("coordinator must return exactly one plan")
	}
	tasks := make([]DAGTask, 0, len(plan.Tasks))
	for _, p := range plan.Tasks {
		request := p.Worker
		tasks = append(tasks, DAGTask{ID: p.ID, Description: p.Description, Dependencies: p.Dependencies, Worker: request, RequestedWorker: &request, Status: "pending"})
	}
	return tasks, ValidateDAG(tasks, config)
}

func (r Runner) Plan(ctx context.Context, config Config, objective string) (planned []DAGTask, planErr error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if r.Resolve == nil {
		r.Resolve = ResolveRuntime
	}
	profile, _ := config.Profile(Primary)
	// Planning remains read-only even when execution has full access.
	profile.FullAccess, profile.AllowEdits, profile.AllowCommands = false, false, false
	r = r.traced(config, profile, "planning")
	r.emit(Primary, "started", objective, workerruntime.Usage{})
	defer func() { r.finish(profile, planErr) }()
	worker, err := r.Resolve(profile)
	if err != nil {
		return nil, err
	}
	if !worker.Capabilities().ReadOnly {
		return nil, errors.New("coordinator backend must support read-only execution")
	}
	routes, _ := json.Marshal(config.Profiles)
	instructions := `You coordinate a parallel software task graph. Decompose the objective into 1–32 concrete, bounded tasks. Independent tasks run concurrently in separate Git worktrees. Dependent tasks start from the combined commits of ALL transitive dependencies. Avoid overlapping file edits in independent tasks; add dependencies where needed. Each task should include explicit acceptance checks and return a concise summary. Workers use the existing primary, middle, or fast profiles, each independently configurable. Prefer middle for implementation and fast for simple mechanical work. Do not edit files, run commands, delegate, or produce patches now. Return ONLY JSON: {"tasks":[{"id":"task-a","description":"Detailed assignment and checks","dependencies":[],"worker":{"profile":"middle"}}]}. Use only these configured profiles; backend overrides are optional and must use existing configured routes. The user reviews the plan before execution. Configured profiles: ` + string(routes)
	instructions += "\nSelect workers by profile only unless a task intentionally needs a different configured route. Do not copy a profile's CLI, model or endpoint into overrides; omitted fields follow subsequent Run settings changes."
	if profile.Instructions != "" {
		instructions += "\nAdditional coordinator guidance (the task-graph JSON contract still applies):\n" + profile.Instructions
	}
	var tasks []DAGTask
	sessionID := r.SessionID
	input := objective
	for attempt := 0; attempt < 2; attempt++ {
		r.trace.Step = attempt + 1
		r.emit(Primary, "input", input, workerruntime.Usage{})
		result, runErr := worker.Execute(ctx, workerruntime.Request{SessionID: sessionID, Model: profile.Model, ReasoningEffort: profile.ReasoningEffort, SystemPrompt: instructions, Task: input, WorkspaceRoot: config.Workspace, Limits: workerruntime.Limits{MaxInputBytes: profile.MaxInputBytes, MaxOutputTokens: profile.MaxOutputTokens, Timeout: profile.Timeout()}, Progress: func(event workerruntime.ProgressEvent) {
			if event.SessionID != "" && worker.Capabilities().PersistentSession {
				sessionID = event.SessionID
				r.trace.SessionID = sessionID
			}
			r.emit(Primary, event.Kind, event.Text, workerruntime.Usage{})
		}})
		if result != nil && result.SessionID != "" && worker.Capabilities().PersistentSession {
			sessionID = result.SessionID
			r.trace.SessionID = sessionID
			r.emit(Primary, "session", "Planning session is available for inspection.", workerruntime.Usage{})
		}
		if runErr != nil {
			if result != nil && (result.Content != "" || result.Usage.Source == "provider") {
				r.emit(Primary, "output", result.Content, result.Usage)
			}
			return nil, workerError(ctx, profile, "planning", runErr)
		}
		if result == nil {
			return nil, errors.New("coordinator returned no plan")
		}
		r.emit(Primary, "output", result.Content, result.Usage)
		tasks, err = parsePlan(result.Content, config)
		if err == nil {
			return tasks, nil
		}
		instructions += "\nThe previous plan was invalid: " + err.Error() + ". Return a corrected JSON object; no prose."
		if sessionID != "" {
			input = "Correct the previous task graph: " + err.Error() + ". Return only the corrected JSON plan."
		}
		r.emit(Primary, "error", "Invalid plan: "+err.Error(), workerruntime.Usage{})
	}
	return nil, err
}
