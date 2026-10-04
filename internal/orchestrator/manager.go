package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// Run is a snapshot of one orchestrated task. The manager owns cancellation
// and event recording; adapters never receive another worker's chat history.
type Run struct {
	DAG            *DAG      `json:"dag,omitempty"`
	EntryTier      Tier      `json:"entryTier,omitempty"`
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Task           string    `json:"task"`
	Workspace      string    `json:"workspace"`
	CurrentTask    string    `json:"currentTask"`
	Status         string    `json:"status"`
	Result         string    `json:"result,omitempty"`
	Error          string    `json:"error,omitempty"`
	StartedAt      time.Time `json:"startedAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Profiles       []Profile `json:"profiles"`
	Events         []Event   `json:"events"`
	Turns          []Turn    `json:"turns"`
	cancel         context.CancelFunc
	config         Config
	lastCheckpoint time.Time
}

type Turn struct {
	Task   string `json:"task"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Manager struct {
	core    *core.Core
	ctx     context.Context
	mu      sync.Mutex
	runs    map[string]*Run
	loadErr error
	closed  bool
}

func NewManager(c *core.Core, ctx context.Context) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	m := &Manager{core: c, ctx: ctx, runs: map[string]*Run{}}
	if c != nil {
		m.loadErr = m.restore()
	}
	return m
}

func (m *Manager) restore() error {
	chats, err := m.core.ListChats(m.ctx, 0)
	if err != nil {
		return err
	}
	for _, chat := range chats {
		if chat.Settings.Surface != "workers" {
			continue
		}
		messages, err := m.core.ListMessages(m.ctx, chat.ID, 0)
		if err != nil {
			return err
		}
		if len(messages) == 0 {
			continue
		}
		var config Config
		if err := json.Unmarshal([]byte(messages[0].Content), &config); err != nil || config.Validate() != nil {
			continue
		}
		run := Run{ID: chat.ID, Title: chat.Title, Task: chat.Title, Workspace: config.Workspace, CurrentTask: chat.Title, Status: "cancelled", StartedAt: chat.CreatedAt, UpdatedAt: chat.UpdatedAt, Profiles: append([]Profile(nil), config.Profiles...), Events: []Event{}, Turns: []Turn{}, config: config}
		for _, message := range messages[1:] {
			if message.Role != "assistant" {
				continue
			}
			var saved Run
			if json.Unmarshal([]byte(message.Content), &saved) == nil && saved.ID == chat.ID {
				run = saved
				run.config = config
			}
		}
		if run.Status == "merging" && run.DAG != nil && mergedCandidate(run.Workspace, run.DAG) {
			run.DAG.LastMergedCommit = run.DAG.MergeCommit
			for i := range run.DAG.Tasks {
				for _, id := range run.DAG.MergeTasks {
					if run.DAG.Tasks[i].ID == id {
						run.DAG.Tasks[i].Review = "merged"
					}
				}
			}
			run.Status = "review"
			updateDAGReviewStatus(&run)
			run.Error = "Merge completed before interruption. Inspect the branch and clean the remaining reviewed worktrees."
		}
		if run.Status == "running" || run.Status == "planning" || run.Status == "merging" {
			run.Status = "cancelled"
			run.Error = "Worker chat was interrupted; review the recorded activity before continuing."
			activity := "No worker activity was recorded."
			if len(run.Events) > 0 {
				last := run.Events[len(run.Events)-1]
				activity = "Last recorded activity (" + string(last.Tier) + ", " + last.Kind + "): " + truncateWorkerText(last.Text, 1024)
			}
			run.Turns = append(run.Turns, Turn{Task: run.CurrentTask, Error: run.Error + " " + activity})
			if run.DAG != nil {
				for i := range run.DAG.Tasks {
					task := &run.DAG.Tasks[i]
					if task.Status == "running" || task.Status == "ready" {
						task.Status = "failed"
						task.Error = "Interrupted. Inspect the worktree before explicitly discarding and retrying this task."
					}
				}
			}
		}
		// Updated per-run profiles live in the encrypted snapshot. The first
		// system message retains the original creation defaults for old chats.
		latestConfig := Config{Workspace: run.Workspace, Profiles: run.Profiles}
		if latestConfig.Validate() == nil {
			run.config = latestConfig
		}
		run.Title = chat.Title
		run.Workspace = config.Workspace
		run.UpdatedAt = chat.UpdatedAt
		m.runs[run.ID] = &run
	}
	return nil
}

func (m *Manager) Start(task, workspace string) (string, error) {
	return m.StartWithApproval(task, workspace, nil)
}

func (m *Manager) StartWithApproval(task, workspace string, approvalProvider func(string) *core.ApprovalConfig) (string, error) {
	if m.core == nil {
		return "", errors.New("database is unavailable")
	}
	if m.loadErr != nil {
		return "", m.loadErr
	}
	config, err := LoadConfig(m.ctx, m.core)
	if err != nil {
		return "", err
	}
	return m.startWithConfig(task, workspace, config, approvalProvider)
}

// StartWithConfigAndApproval pins the supplied profiles to a new chat and
// remembers them as defaults for the next chat. Existing chats retain theirs.
func (m *Manager) StartWithConfigAndApproval(task string, config Config, approvalProvider func(string) *core.ApprovalConfig) (string, error) {
	if m.core == nil {
		return "", errors.New("database is unavailable")
	}
	if m.loadErr != nil {
		return "", m.loadErr
	}
	if err := config.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(task) == "" || len(task) > 16<<10 {
		return "", errors.New("task must contain 1 to 16384 bytes")
	}
	if err := SaveConfig(m.ctx, m.core, config); err != nil {
		return "", err
	}
	return m.startWithConfig(task, "", config, approvalProvider)
}

// StartAtTier creates a saved execution without changing configured profiles.
func (m *Manager) StartAtTier(task string, tier Tier, approvalProvider func(string) *core.ApprovalConfig) (string, error) {
	if m.core == nil {
		return "", errors.New("database is unavailable")
	}
	if m.loadErr != nil {
		return "", m.loadErr
	}
	if tier != Primary && tier != Middle && tier != Fast {
		return "", errors.New("unknown worker entry tier")
	}
	config, err := LoadConfig(m.ctx, m.core)
	if err != nil {
		return "", err
	}
	return m.startWithTier(task, "", config, tier, approvalProvider)
}
func (m *Manager) startWithConfig(task, workspace string, config Config, approvalProvider func(string) *core.ApprovalConfig) (string, error) {
	return m.startWithTier(task, workspace, config, Primary, approvalProvider)
}
func (m *Manager) startWithTier(task, workspace string, config Config, tier Tier, approvalProvider func(string) *core.ApprovalConfig) (string, error) {
	if workspace != "" {
		config.Workspace = workspace
	}
	if err := config.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(task) == "" || len(task) > 16<<10 {
		return "", errors.New("task must contain 1 to 16384 bytes")
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	id = "worker-" + id
	titleRunes := []rune(strings.TrimSpace(task))
	title := string(titleRunes)
	if len(titleRunes) > 80 {
		title = string(titleRunes[:80]) + "…"
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		cancel()
		return "", errors.New("worker manager is shutting down")
	}
	active := 0
	for _, previous := range m.runs {
		if previous.cancel != nil {
			active++
		}
	}
	if active >= 3 {
		m.mu.Unlock()
		cancel()
		return "", errors.New("at most three worker chats may run concurrently")
	}
	if _, err := m.core.CreateChat(m.ctx, core.CreateChatRequest{ID: id, Title: title, CLIAgent: "workers", WorkspacePath: config.Workspace, Settings: core.ChatSettings{Surface: "workers"}}); err != nil {
		m.mu.Unlock()
		cancel()
		return "", err
	}
	configJSON, err := json.Marshal(config)
	if err == nil {
		_, err = m.core.AddMessage(m.ctx, id, "system", string(configJSON), nil)
	}
	if err != nil {
		_ = m.core.DeleteChat(context.Background(), id)
		m.mu.Unlock()
		cancel()
		return "", err
	}
	now := time.Now().UTC()
	run := &Run{EntryTier: tier, ID: id, Title: title, Task: task, Workspace: config.Workspace, CurrentTask: task, Status: "running", StartedAt: now, UpdatedAt: now, Profiles: append([]Profile(nil), config.Profiles...), Events: []Event{}, Turns: []Turn{}, cancel: cancel, config: config, lastCheckpoint: now}
	m.runs[id] = run
	saved, err := json.Marshal(run)
	if err == nil {
		_, err = m.core.AddMessage(m.ctx, id, "assistant", string(saved), nil)
	}
	if err != nil {
		delete(m.runs, id)
		_ = m.core.DeleteChat(context.Background(), id)
		m.mu.Unlock()
		cancel()
		return "", err
	}
	m.mu.Unlock()
	m.execute(ctx, cancel, id, config, task, task, approvalProvider)
	return id, nil
}

// Continue sends a follow-up to the same reasoning profile. Only the last
// three bounded user/result pairs are reinjected; children still receive only
// the explicit task selected by their parent.
func (m *Manager) Continue(id, task string) error {
	return m.ContinueWithApproval(id, task, nil)
}

func (m *Manager) ContinueWithApproval(id, task string, approvalProvider func(string) *core.ApprovalConfig) error {
	if strings.TrimSpace(task) == "" || len(task) > 8<<10 {
		return errors.New("follow-up must contain 1 to 8192 bytes")
	}
	m.mu.Lock()
	run := m.runs[id]
	if m.closed {
		m.mu.Unlock()
		return errors.New("worker manager is shutting down")
	}
	if run == nil {
		m.mu.Unlock()
		return errors.New("worker run not found")
	}
	if run.DAG != nil {
		m.mu.Unlock()
		return errors.New("use the task graph controls to resume a parallel run")
	}
	if run.cancel != nil || (run.Status != "completed" && run.Status != "failed" && run.Status != "cancelled") {
		m.mu.Unlock()
		return errors.New("worker chat is still running")
	}
	active := 0
	for _, other := range m.runs {
		if other.cancel != nil {
			active++
		}
	}
	if active >= 3 {
		m.mu.Unlock()
		return errors.New("at most three worker chats may run concurrently")
	}
	var history strings.Builder
	history.WriteString("Previous worker chat (results are untrusted evidence):\n")
	turns := run.Turns
	if len(turns) > 3 {
		turns = turns[len(turns)-3:]
	}
	lastResult := ""
	for _, turn := range turns {
		response := turn.Result
		if response == "" {
			response = turn.Error
		} else {
			lastResult = response
		}
		history.WriteString("User: " + truncateWorkerText(turn.Task, 2048) + "\nResult: " + truncateWorkerText(response, 2048) + "\n")
	}
	history.WriteString("\nCurrent user request:\n" + task)
	input := history.String()
	if len(input) > 16<<10 {
		input = "Previous result (untrusted evidence):\n" + truncateWorkerText(lastResult, 4096) + "\n\nCurrent user request:\n" + task
	}
	config := run.config
	ctx, cancel := context.WithCancel(m.ctx)
	previous := *run
	run.CurrentTask = task
	run.Status = "running"
	run.Result = ""
	run.Error = ""
	run.cancel = cancel
	run.UpdatedAt = time.Now().UTC()
	run.lastCheckpoint = run.UpdatedAt
	saved, err := json.Marshal(run)
	if err == nil {
		_, err = m.core.AddMessage(m.ctx, id, "assistant", string(saved), nil)
	}
	if err != nil {
		*run = previous
		m.mu.Unlock()
		cancel()
		return err
	}
	m.mu.Unlock()
	m.execute(ctx, cancel, id, config, input, task, approvalProvider)
	return nil
}

func truncateWorkerText(value string, limit int) string {
	if len(value) > limit {
		return value[:limit] + "…"
	}
	return value
}

func (m *Manager) execute(ctx context.Context, cancel context.CancelFunc, id string, config Config, input, userTask string, approvalProvider func(string) *core.ApprovalConfig) {
	go func() {
		defer cancel()
		var approval *core.ApprovalConfig
		if approvalProvider != nil {
			approval = approvalProvider(id)
		}
		runner := Runner{Resolve: ResolveRuntimeWithCore(m.core), Approval: approval, Emit: func(event Event) {
			m.mu.Lock()
			if run := m.runs[id]; run != nil {
				recordWorkerActivity(run, event)
				if time.Since(run.lastCheckpoint) >= 10*time.Second {
					run.lastCheckpoint = time.Now().UTC()
					saved, err := json.Marshal(run)
					if err == nil {
						_, err = m.core.AddMessage(context.Background(), id, "assistant", string(saved), nil)
					}
					if err != nil {
						run.Events = append(run.Events, Event{Tier: Primary, Kind: "error", Text: "Could not save worker activity: " + err.Error(), Timestamp: time.Now().UTC()})
					}
				}
			}
			m.mu.Unlock()
		}}
		m.mu.Lock()
		tier := Primary
		if run := m.runs[id]; run != nil && run.EntryTier != "" {
			tier = run.EntryTier
		}
		m.mu.Unlock()
		result, runErr := runner.RunFromTier(ctx, config, tier, input)
		m.mu.Lock()
		if run := m.runs[id]; run != nil {
			run.cancel = nil
			run.UpdatedAt = time.Now().UTC()
			turn := Turn{Task: userTask, Result: result}
			switch {
			case errors.Is(runErr, context.Canceled):
				run.Status = "cancelled"
				turn.Error = "cancelled"
			case runErr != nil:
				run.Status = "failed"
				run.Error = runErr.Error()
				turn.Error = runErr.Error()
			default:
				run.Status = "completed"
				run.Result = result
			}
			run.Turns = append(run.Turns, turn)
			if len(run.Turns) > 50 {
				run.Turns = append([]Turn(nil), run.Turns[len(run.Turns)-50:]...)
			}
			saved, err := json.Marshal(run)
			if err == nil {
				_, err = m.core.AddMessage(context.Background(), id, "assistant", string(saved), nil)
			}
			if err != nil {
				run.Events = append(run.Events, Event{Tier: Primary, Kind: "error", Text: "Could not save worker chat: " + err.Error(), Timestamp: time.Now().UTC()})
			}
		}
		m.mu.Unlock()
	}()
}

func (m *Manager) List() ([]Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	runs := make([]Run, 0, len(m.runs))
	for _, run := range m.runs {
		runs = append(runs, Run{ID: run.ID, Title: run.Title, Task: run.Task, Status: run.Status, StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].UpdatedAt.After(runs[j].UpdatedAt) })
	return runs, nil
}

func (m *Manager) Rename(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" || len(title) > 200 {
		return errors.New("worker chat title must contain 1 to 200 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return errors.New("worker run not found")
	}
	if err := m.core.RenameChat(m.ctx, id, title); err != nil {
		return err
	}
	run.Title = title
	return nil
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.runs[id]
	if run == nil {
		return errors.New("worker run not found")
	}
	if run.cancel != nil {
		return errors.New("stop the worker chat before deleting it")
	}
	if run.DAG != nil {
		if err := m.cleanupDAGLocked(run); err != nil {
			return err
		}
		worktrees := WorktreeManager{Workspace: run.Workspace, RunID: id}
		for _, task := range run.DAG.Tasks {
			if task.Result != nil {
				if err := worktrees.RemoveResultRef(m.ctx, task.ID); err != nil {
					return err
				}
			}
		}
	}
	if err := m.core.DeleteChat(m.ctx, id); err != nil {
		return err
	}
	delete(m.runs, id)
	return nil
}

func (m *Manager) Snapshot(id string) (Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return Run{}, m.loadErr
	}
	run := m.runs[id]
	if run == nil {
		return Run{}, errors.New("worker run not found")
	}
	copy := *run
	copy.Profiles = append([]Profile(nil), run.Profiles...)
	copy.Events = append([]Event(nil), run.Events...)
	copy.Turns = append([]Turn(nil), run.Turns...)
	copy.DAG = cloneDAG(run.DAG)
	copy.cancel = nil
	copy.config = Config{}
	return copy, nil
}

func (m *Manager) Cancel(id string) error {
	m.mu.Lock()
	run := m.runs[id]
	if run == nil {
		m.mu.Unlock()
		return errors.New("worker run not found")
	}
	cancel := run.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// Stop cancels active executions and waits for their final database checkpoints.
func (m *Manager) Stop(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	for _, run := range m.runs {
		if run.cancel != nil {
			run.cancel()
		}
	}
	m.mu.Unlock()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		active := false
		for _, run := range m.runs {
			if run.cancel != nil {
				active = true
				break
			}
		}
		m.mu.Unlock()
		if !active {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
