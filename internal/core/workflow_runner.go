package core

import (
	"context"
	"errors"
	"fmt"
	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"strings"
	"time"
)

// RunOptions parameterise one workflow execution.
type RunOptions struct {
	// Agent is the agent whose workflow should run. Required.
	Agent *Agent

	// WorkflowName, if empty, uses agent.ResolveDefaultWorkflow().
	WorkflowName string

	// Inputs maps WorkflowInput.Name → user-supplied value.
	Inputs map[string]string

	// CLI is the third-party CLI to drive (claude, codex, ...).
	// Must appear in agent.Supports or the run is refused.
	CLI string

	// Cwd is the working directory the CLI sees.
	Cwd string

	// Env is per-launch environment overrides (e.g. local-LLM routing).
	Env map[string]string

	// Model, if non-empty, selects the model for every workflow turn.
	Model string

	// Tools selects the same permission level used by interactive chats.
	// Empty is the safe default; workflows never elevate it implicitly.
	Tools string

	// OnTurn, if non-nil, is invoked with each completed turn as it
	// arrives. Lets a TUI/GUI stream the conversation without waiting
	// for the whole workflow to finish.
	OnTurn func(turn TurnResult)

	// OnEvent, if non-nil, is invoked with live turn events and workflow
	// state transitions. CLIs without streaming still emit state events.
	OnEvent func(event WorkflowRunEvent)

	// Persist toggles DB-backed chat persistence. When true the runner
	// creates a chats row at start, writes one messages row per turn
	// (user + assistant), and calls EndChat at completion with the
	// outcome mapped to chats.exit_kind.
	//
	// Persistence is silently skipped when Core has no store (e.g.
	// in legacy launcher-only configurations).
	Persist bool

	// ChatTitle, if Persist is true, becomes chats.title. Falls back
	// to the agent + workflow name.
	ChatTitle string

	// SystemContext, if non-empty, is appended to the workflow's system
	// prompt. GUI callers use this to make per-run context explicit
	// without changing agent YAML.
	SystemContext string

	// ChatSettings, if Persist is true, seeds the temporary/persisted chat
	// row created for the workflow transcript.
	ChatSettings ChatSettings
}

// TurnResult records one round of (user message, assistant reply).
type TurnResult struct {
	Index        int
	WorkflowName string
	UserMsg      string
	Reply        *Reply
	DurationMs   int64
}

// WorkflowRunEvent is one live update from a workflow run.
type WorkflowRunEvent struct {
	WorkflowName string
	TurnIndex    int
	Type         string
	Text         string
	Tool         string
	Detail       string
	ID           string
	OK           bool
}

// RunAllOptions parameterise a sequential, shared-session execution of
// every workflow declared on an agent.
type RunAllOptions struct {
	Agent            *Agent
	InputsByWorkflow map[string]map[string]string
	CLI              string
	Cwd              string
	Env              map[string]string
	Model            string
	Tools            string
	OnTurn           func(turn TurnResult)
	OnEvent          func(event WorkflowRunEvent)
	Persist          bool
	ChatTitle        string
	SystemContext    string
	ChatSettings     ChatSettings
}

// RunResult is the terminal outcome of a workflow execution.
type RunResult struct {
	AgentID      string
	WorkflowName string
	RunID        string // managed-runtime run ID; empty for native CLI workflows
	Turns        []TurnResult
	SessionID    string // last seen session id from the adapter
	Outcome      RunOutcome
	Err          error

	// ChatID is non-empty when Persist was set and a chat row was
	// successfully created. GUI workflow chats are temporary and deleted
	// when the run view is closed.
	ChatID string
}

// RunOutcome enumerates how a workflow can end. Mirrors the exit
// taxonomy from plan §3 / Osaurus's exit_kind.
type RunOutcome string

const (
	OutcomeCompleted   RunOutcome = "completed"
	OutcomeCancelled   RunOutcome = "cancelled"
	OutcomeAgentFailed RunOutcome = "agent_failed"
	OutcomeAdapterErr  RunOutcome = "adapter_error"
)

// RunWorkflow executes opts.Workflow against opts.CLI using the
// option-C hybrid strategy:
//
//   - Step 0 always runs via adapter.SingleShot.
//   - Subsequent user_message steps use adapter.Resume if the adapter
//     supports it (cheaper: keeps state inside the CLI), or fall back
//     to SingleShot per turn.
//
// wait_for_assistant steps are explicit barriers: by the time the
// runner observes one, the preceding user_message has already received
// and persisted its final reply. until_tool is retained for YAML
// compatibility; specific tool-gated waits can be layered on later.
func (c *Core) RunWorkflow(ctx context.Context, opts RunOptions) *RunResult {
	if err := validateRunOptions(opts); err != nil {
		return &RunResult{Outcome: OutcomeAdapterErr, Err: err}
	}
	workflow := opts.Agent.FindWorkflow(opts.WorkflowName)
	if workflow == nil {
		workflow = opts.Agent.ResolveDefaultWorkflow()
		if workflow == nil {
			return &RunResult{
				AgentID: opts.Agent.ID, WorkflowName: opts.WorkflowName,
				Outcome: OutcomeAdapterErr,
				Err:     fmt.Errorf("agent %q: no workflow named %q and no default", opts.Agent.ID, opts.WorkflowName),
			}
		}
	}
	return c.runWorkflowSequence(ctx, workflowRunConfig{
		Agent: opts.Agent, CLI: opts.CLI, Cwd: opts.Cwd, Env: opts.Env,
		Model: opts.Model, Tools: opts.Tools, OnTurn: opts.OnTurn, OnEvent: opts.OnEvent,
		Persist: opts.Persist, ChatTitle: opts.ChatTitle, SystemContext: opts.SystemContext,
		ChatSettings: opts.ChatSettings,
	}, []workflowRunPlan{{Workflow: workflow, Inputs: opts.Inputs}})
}

// RunAllWorkflows executes every workflow on opts.Agent in YAML order,
// sharing the adapter session between workflows. Because shared context is
// the point of this path, it requires a resumable CLI when there is more
// than one workflow.
func (c *Core) RunAllWorkflows(ctx context.Context, opts RunAllOptions) *RunResult {
	if err := validateRunAllOptions(opts); err != nil {
		return &RunResult{Outcome: OutcomeAdapterErr, Err: err}
	}
	plans := make([]workflowRunPlan, 0, len(opts.Agent.Workflows))
	for i := range opts.Agent.Workflows {
		w := &opts.Agent.Workflows[i]
		plans = append(plans, workflowRunPlan{
			Workflow: w,
			Inputs:   opts.InputsByWorkflow[w.Name],
		})
	}
	return c.runWorkflowSequence(ctx, workflowRunConfig{
		Agent: opts.Agent, CLI: opts.CLI, Cwd: opts.Cwd, Env: opts.Env,
		Model: opts.Model, Tools: opts.Tools, OnTurn: opts.OnTurn, OnEvent: opts.OnEvent,
		Persist: opts.Persist, ChatTitle: opts.ChatTitle, SystemContext: opts.SystemContext,
		ChatSettings: opts.ChatSettings, RequireResume: true,
	}, plans)
}

type workflowRunPlan struct {
	Workflow *Workflow
	Inputs   map[string]string
}

type workflowRunConfig struct {
	Agent         *Agent
	CLI           string
	Cwd           string
	Env           map[string]string
	Model         string
	Tools         string
	OnTurn        func(turn TurnResult)
	OnEvent       func(event WorkflowRunEvent)
	Persist       bool
	ChatTitle     string
	SystemContext string
	ChatSettings  ChatSettings
	RequireResume bool
}

func (c *Core) runWorkflowSequence(ctx context.Context, cfg workflowRunConfig, plans []workflowRunPlan) *RunResult {
	// One task identity for all workflow subruns; an external caller may supply
	// its durable retry identity before entering this sequence.
	if skillTaskBudgetID(ctx, "") == "" {
		id, err := agentic.NewRunID()
		if err != nil {
			return &RunResult{Outcome: OutcomeAdapterErr, Err: err}
		}
		ctx = WithSkillTaskBudget(ctx, "workflow:"+id)
	}
	res := &RunResult{Outcome: OutcomeAdapterErr}
	if err := validateWorkflowRunConfig(cfg); err != nil {
		res.Err = err
		return res
	}
	if len(plans) == 0 {
		res.AgentID = cfg.Agent.ID
		res.Err = fmt.Errorf("agent %q: no workflows to run", cfg.Agent.ID)
		return res
	}
	if plans[0].Workflow != nil {
		res.WorkflowName = plans[0].Workflow.Name
	}
	res.AgentID = cfg.Agent.ID

	for _, p := range plans {
		if p.Workflow == nil {
			res.Err = fmt.Errorf("agent %q: nil workflow in run plan", cfg.Agent.ID)
			return res
		}
		if err := c.guardNewBoundSkills(ctx, cfg.Agent, p.Workflow, cfg.ChatSettings); err != nil {
			res.Err = err
			return res
		}
	}

	if !contains(cfg.Agent.Supports, cfg.CLI) {
		res.Err = fmt.Errorf("agent %q does not support CLI %q (supports: %v)",
			cfg.Agent.ID, cfg.CLI, cfg.Agent.Supports)
		return res
	}
	runtimeConfig, err := c.ResolveEffectiveAgentConfig(ctx, cfg.Agent)
	if err != nil {
		res.Err = err
		return res
	}
	if runtimeConfig.Mode == RuntimeAgentic {
		return c.runManagedWorkflowSequence(ctx, cfg, plans, res)
	}
	for _, plan := range plans {
		if len(plan.Workflow.FinishEvidence) > 0 {
			res.Err = fmt.Errorf("workflow %q requires managed artifact verification; native CLI finish is not observable", plan.Workflow.Name)
			return res
		}
	}

	adapter, err := GetCLIAdapter(cfg.CLI)
	if err != nil {
		res.Err = err
		return res
	}
	if cfg.RequireResume && !adapter.SupportsResume() {
		res.Err = fmt.Errorf("agent %q: run all workflows requires resumable CLI %q", cfg.Agent.ID, cfg.CLI)
		return res
	}
	effective, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{
		Workflow: plans[0].Workflow, SkillSettings: &cfg.ChatSettings,
		Surface: SurfaceWorkflow, Agent: cfg.Agent, CLI: cfg.CLI, Cwd: cfg.Cwd,
		Model: cfg.Model, Tools: cfg.Tools, ToolsConfigured: true,
		Local: cfg.ChatSettings.Local,
	})
	if err != nil {
		res.Err = err
		return res
	}
	if err := c.PrepareExecution(ctx, effective); err != nil {
		res.Err = err
		return res
	}
	cfg.Model = effective.Model
	cfg.Tools = effective.Tools
	cfg.Env = mergeStringMaps(cfg.Env, effective.Env)

	rendered := make([]*RenderedWorkflow, 0, len(plans))
	for _, p := range plans {
		rw, err := RenderWorkflow(cfg.Agent, p.Workflow, p.Inputs)
		if err != nil {
			res.Err = err
			return res
		}
		rendered = append(rendered, rw)
	}
	privacy := c.PrivacyScanner().NewRedactionSession()
	systemPrompt := withSystemContext(AgentSystemPrompt(cfg.Agent), WorkflowSystemContext(cfg.Cwd))
	systemPrompt = withSystemContext(systemPrompt, cfg.SystemContext)
	systemPrompt, _ = privacy.Redact(systemPrompt)

	// Optional DB-backed chat persistence. Failures here are logged
	// via res.Err only if no other path sets it later; they do NOT
	// abort the run — a user shouldn't lose a workflow because the
	// DB hiccupped.
	chatID := c.maybeCreateChat(ctx, RunOptions{
		Agent: cfg.Agent, WorkflowName: res.WorkflowName, CLI: cfg.CLI,
		Cwd: cfg.Cwd, Model: cfg.Model, Tools: cfg.Tools, Persist: cfg.Persist, ChatTitle: cfg.ChatTitle,
		ChatSettings: cfg.ChatSettings,
	})
	res.ChatID = chatID

	turnIdx := 0
	var lastReply *Reply
	var previousResults strings.Builder
	previousV2 := false
	tools := cfg.Tools
	for workflowIndex, workflow := range rendered {
		settings, err := c.workflowSkillSettings(ctx, cfg.Agent, plans[workflowIndex].Workflow, cfg.ChatSettings)
		if err != nil {
			res.Err = err
			return res
		}
		v2 := settings.SkillsV2 != nil
		if workflowIndex > 0 && (v2 || previousV2) {
			lastReply = nil
		}
		previousV2 = v2
		if chatID != "" && v2 {
			if err := c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) {
				s.SkillsV2 = settings.SkillsV2
				s.SkillsLock = settings.SkillsLock
				s.Skills = nil
			}); err != nil {
				res.Err = err
				return res
			}
		}
		emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
			WorkflowName: workflow.Name, Type: "workflow_start", OK: true,
		})
		for _, step := range workflow.Steps {
			if step.Kind == StepWaitForAssistant {
				barrierTurnIdx := turnIdx
				if barrierTurnIdx > 0 {
					barrierTurnIdx--
				}
				emitWaitBarrier(cfg.OnEvent, workflow.Name, barrierTurnIdx, step.UntilTool)
				continue
			}
			if step.Kind != StepUserMessage {
				continue
			}
			if err := ctx.Err(); err != nil {
				res.Outcome = OutcomeCancelled
				res.Err = err
				c.maybeEndChat(ctx, chatID, res.Outcome)
				return res
			}

			body := step.Body

			adapterBody := body
			if workflowIndex > 0 && lastReply == nil && previousResults.Len() > 0 {
				adapterBody = "Previous workflow results (task context):\n" + previousResults.String() + "\nCurrent task:\n" + adapterBody
			}
			adapterBody, _ = privacy.Redact(adapterBody)
			skillPayload, skillState, skillErr := c.BuildChatSkillPayload(ctx, settings, systemPrompt+"\n"+adapterBody)
			if skillErr != nil {
				res.Err = skillErr
				c.maybeEndChat(ctx, chatID, res.Outcome)
				return res
			}
			turnSystem := systemPrompt
			if skillPayload != "" && lastReply == nil {
				turnSystem = withSystemContext(turnSystem, skillPayload)
			}
			if skillPayload != "" && lastReply != nil {
				adapterBody = skillPayload + "\n\n" + adapterBody
			}
			if err := validateControlledSkillRequest(settings, turnSystem, adapterBody); err != nil {
				res.Err = err
				c.maybeEndChat(ctx, chatID, res.Outcome)
				return res
			}
			if settings.SkillsV2 != nil {
				if err := c.reserveSkillTaskInput(ctx, skillTaskBudgetID(ctx, "workflow:"+res.ChatID), int64(len(turnSystem)+len(adapterBody)), int64(len(skillPayload)), 8<<20); err != nil {
					res.Err = err
					return res
				}
			}
			c.maybeAddMessage(ctx, chatID, "user", body)

			emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
				WorkflowName: workflow.Name, TurnIndex: turnIdx, Type: "turn_start", OK: true,
			})

			start := time.Now()
			reply, runErr := runWorkflowTurn(ctx, adapter, workflow.Name, turnIdx, cfg, tools,
				turnSystem, adapterBody, lastReply, cfg.OnEvent)
			if runErr != nil {
				res.Outcome = OutcomeAdapterErr
				res.Err = runErr
				emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
					WorkflowName: workflow.Name, TurnIndex: turnIdx, Type: "error", Detail: runErr.Error(), OK: false,
				})
				c.maybeEndChat(ctx, chatID, res.Outcome)
				return res
			}
			reply = revealReply(privacy, reply)
			if reply.ExitCode != 0 {
				res.Outcome = OutcomeAgentFailed
				res.Err = fmt.Errorf("%s exited with code %d", adapter.Name(), reply.ExitCode)
				emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
					WorkflowName: workflow.Name, TurnIndex: turnIdx, Type: "error", Detail: res.Err.Error(), OK: false,
				})
				res.Turns = append(res.Turns, TurnResult{
					Index:        turnIdx,
					WorkflowName: workflow.Name,
					UserMsg:      body,
					Reply:        reply,
					DurationMs:   time.Since(start).Milliseconds(),
				})
				c.maybeAddMessage(ctx, chatID, "assistant", reply.Text)
				c.maybeEndChat(ctx, chatID, res.Outcome)
				return res
			}

			c.maybeAddMessage(ctx, chatID, "assistant", reply.Text)

			turn := TurnResult{
				Index:        turnIdx,
				WorkflowName: workflow.Name,
				UserMsg:      body,
				Reply:        reply,
				DurationMs:   time.Since(start).Milliseconds(),
			}
			res.Turns = append(res.Turns, turn)
			if cfg.OnTurn != nil {
				cfg.OnTurn(turn)
			}
			emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
				WorkflowName: workflow.Name, TurnIndex: turnIdx, Type: "turn_finish", OK: true,
			})
			lastReply = reply
			previousResults.WriteString(workflow.Name + ":\n" + reply.Text + "\n")
			if chatID != "" && skillState != nil {
				skillState.Status = "delivered"
				_ = c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) { s.SkillRuntime = skillState })
			}
			turnIdx++
		}
		emitWorkflowEvent(cfg.OnEvent, WorkflowRunEvent{
			WorkflowName: workflow.Name, TurnIndex: turnIdx, Type: "workflow_finish", OK: true,
		})
	}
	if lastReply != nil {
		res.SessionID = lastReply.SessionID
	}
	// Stamp the session id so a workflow-started chat can be continued
	// interactively (ContinueChat) afterwards.
	if chatID != "" && res.SessionID != "" {
		_ = c.SetChatSessionID(ctx, chatID, res.SessionID)
	}
	res.Outcome = OutcomeCompleted
	c.maybeEndChat(ctx, chatID, res.Outcome)
	return res
}

func runWorkflowTurn(ctx context.Context, adapter CLIAdapter, workflowName string, turnIdx int,
	cfg workflowRunConfig, tools, systemPrompt, adapterBody string, lastReply *Reply,
	onEvent func(WorkflowRunEvent)) (*Reply, error) {
	emit := func(ev StreamEvent) {
		emitWorkflowEvent(onEvent, WorkflowRunEvent{
			WorkflowName: workflowName,
			TurnIndex:    turnIdx,
			Type:         ev.Type,
			Text:         ev.Text,
			Tool:         ev.Tool,
			Detail:       ev.Detail,
			ID:           ev.ID,
			OK:           ev.OK,
		})
	}
	firstTurn := turnIdx == 0 || !adapter.SupportsResume() || lastReply == nil || lastReply.SessionID == ""
	if sc, ok := adapter.(streamingAdapter); ok && onEvent != nil {
		var reply *Reply
		var err error
		if firstTurn {
			reply, err = sc.SingleShotStream(ctx, SingleShotOpts{
				Cwd: cfg.Cwd, Message: adapterBody, SystemPrompt: systemPrompt,
				Model: cfg.Model, Tools: tools, Env: cfg.Env,
			}, emit)
		} else {
			reply, err = sc.ResumeStream(ctx, lastReply.SessionID, ResumeOpts{
				Message: adapterBody, Cwd: cfg.Cwd, Model: cfg.Model, Tools: tools, Env: cfg.Env,
			}, emit)
		}
		if !errors.Is(err, ErrStreamUnsupported) {
			return reply, err
		}
	}
	if firstTurn {
		return adapter.SingleShot(ctx, SingleShotOpts{
			Cwd: cfg.Cwd, Message: adapterBody, SystemPrompt: systemPrompt,
			Model: cfg.Model, Tools: tools, Env: cfg.Env,
		})
	}
	return adapter.Resume(ctx, lastReply.SessionID, ResumeOpts{
		Message: adapterBody, Cwd: cfg.Cwd, Model: cfg.Model, Tools: tools, Env: cfg.Env,
	})
}

func emitWorkflowEvent(onEvent func(WorkflowRunEvent), ev WorkflowRunEvent) {
	if onEvent != nil {
		onEvent(ev)
	}
}

func emitWaitBarrier(onEvent func(WorkflowRunEvent), workflowName string, turnIdx int, untilTool string) {
	detail := "wait_for_assistant"
	if strings.TrimSpace(untilTool) != "" {
		detail += ":" + strings.TrimSpace(untilTool)
	}
	emitWorkflowEvent(onEvent, WorkflowRunEvent{
		WorkflowName: workflowName,
		TurnIndex:    turnIdx,
		Type:         "step_start",
		Detail:       detail,
		OK:           true,
	})
	emitWorkflowEvent(onEvent, WorkflowRunEvent{
		WorkflowName: workflowName,
		TurnIndex:    turnIdx,
		Type:         "step_finish",
		Detail:       detail,
		OK:           true,
	})
}

func revealReply(redaction *PrivacyRedaction, reply *Reply) *Reply {
	if reply == nil {
		return nil
	}
	out := *reply
	out.Text = redaction.Reveal(reply.Text)
	return &out
}

// maybeCreateChat is a no-op when persistence is off, store is nil, or
// the CreateChat call errors. It deliberately swallows DB errors so
// they can't fail a workflow — the worst case is a non-persisted run.
func (c *Core) maybeCreateChat(ctx context.Context, opts RunOptions) string {
	if !opts.Persist || c.store == nil {
		return ""
	}
	title := opts.ChatTitle
	if title == "" {
		title = opts.Agent.Name + " · " + opts.WorkflowName
	}
	settings := opts.ChatSettings
	var err error
	settings, err = freezeBoundSettings(opts.Agent, opts.Agent.FindWorkflow(opts.WorkflowName), settings)
	if err != nil {
		return ""
	}
	settings.Tools = opts.Tools
	if settings.Model == "" {
		settings.Model = opts.Model
	}
	ch, err := c.CreateChat(ctx, CreateChatRequest{
		Title:         title,
		AgentID:       opts.Agent.ID,
		CLIAgent:      opts.CLI,
		WorkspacePath: opts.Cwd,
		Settings:      settings,
	})
	if err != nil {
		return ""
	}
	return ch.ID
}

func (c *Core) maybeAddMessage(ctx context.Context, chatID, role, content string) {
	if chatID == "" || c.store == nil {
		return
	}
	_, _ = c.AddMessage(ctx, chatID, role, content, nil)
}

func (c *Core) maybeEndChat(ctx context.Context, chatID string, outcome RunOutcome) {
	if chatID == "" || c.store == nil {
		return
	}
	_ = c.EndChat(ctx, chatID, string(outcome))
}

func validateRunOptions(opts RunOptions) error {
	if opts.Agent == nil {
		return errors.New("RunWorkflow: nil agent")
	}
	if opts.CLI == "" {
		return errors.New("RunWorkflow: empty CLI")
	}
	if opts.Cwd == "" {
		return errors.New("RunWorkflow: empty Cwd")
	}
	return nil
}

func validateRunAllOptions(opts RunAllOptions) error {
	if opts.Agent == nil {
		return errors.New("RunAllWorkflows: nil agent")
	}
	if opts.CLI == "" {
		return errors.New("RunAllWorkflows: empty CLI")
	}
	if opts.Cwd == "" {
		return errors.New("RunAllWorkflows: empty Cwd")
	}
	return nil
}

func validateWorkflowRunConfig(cfg workflowRunConfig) error {
	if cfg.Agent == nil {
		return errors.New("RunWorkflow: nil agent")
	}
	if cfg.CLI == "" {
		return errors.New("RunWorkflow: empty CLI")
	}
	if cfg.Cwd == "" {
		return errors.New("RunWorkflow: empty Cwd")
	}
	return nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func mergeStringMaps(a, b map[string]string) map[string]string {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
