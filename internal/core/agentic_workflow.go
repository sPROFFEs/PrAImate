package core

import (
	"context"
	"errors"
	"strings"
	"time"
)

func (c *Core) runManagedWorkflowSequence(ctx context.Context, cfg workflowRunConfig, plans []workflowRunPlan, res *RunResult) *RunResult {
	var selections []ChatSettings
	v2 := false
	for _, plan := range plans {
		// Preflight the complete sequence before any model request.
		if _, err := RenderWorkflow(cfg.Agent, plan.Workflow, plan.Inputs); err != nil {
			res.Err = err
			return res
		}
		settings, err := c.workflowSkillSettings(ctx, cfg.Agent, plan.Workflow, cfg.ChatSettings)
		if err != nil {
			res.Err = err
			return res
		}
		selections = append(selections, settings)
		v2 = v2 || settings.SkillsV2 != nil || len(plan.Workflow.FinishEvidence) > 0
	}
	chatID := c.maybeCreateChat(ctx, RunOptions{
		Agent: cfg.Agent, WorkflowName: res.WorkflowName, CLI: cfg.CLI,
		Cwd: cfg.Cwd, Model: cfg.Model, Tools: cfg.Tools, Persist: cfg.Persist,
		ChatTitle: cfg.ChatTitle, ChatSettings: selections[0],
	})
	res.ChatID = chatID
	if !v2 {
		res = c.runManagedWorkflowBatch(ctx, cfg, plans, res, "", 0)
		c.maybeEndChat(ctx, chatID, res.Outcome)
		return res
	}
	var previous strings.Builder
	for i, plan := range plans {
		partCfg := cfg
		partCfg.ChatSettings = selections[i]
		if chatID != "" {
			if err := c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) {
				s.SkillsV2, s.SkillsLock = selections[i].SkillsV2, selections[i].SkillsLock
				s.Skills, s.SkillRuntime = selections[i].Skills, nil
			}); err != nil {
				res.Err = err
				res.Outcome = OutcomeAdapterErr
				break
			}
		}
		part := c.runManagedWorkflowBatch(ctx, partCfg, []workflowRunPlan{plan}, &RunResult{
			AgentID: res.AgentID, WorkflowName: plan.Workflow.Name, ChatID: chatID, Outcome: OutcomeAdapterErr,
		}, previous.String(), len(res.Turns))
		res.Turns = append(res.Turns, part.Turns...)
		res.RunID, res.SessionID, res.Outcome, res.Err = part.RunID, part.SessionID, part.Outcome, part.Err
		if part.Err != nil {
			break
		}
		for _, turn := range part.Turns {
			if turn.Reply != nil {
				previous.WriteString(plan.Workflow.Name + ":\n" + turn.Reply.Text + "\n")
			}
		}
	}
	c.maybeEndChat(ctx, chatID, res.Outcome)
	return res
}

// Legacy sequences remain one managed task. V2 sequences use one bounded run
// per workflow, carrying results as task data, never the previous skill body.
func (c *Core) runManagedWorkflowBatch(ctx context.Context, cfg workflowRunConfig, plans []workflowRunPlan, res *RunResult, previous string, turnIndex int) *RunResult {
	redaction := c.PrivacyScanner().NewRedactionSession()
	var task strings.Builder
	if previous != "" {
		task.WriteString("Previous workflow results (task context):\n" + previous + "\nCurrent task:\n")
	}
	for _, plan := range plans {
		rendered, err := RenderWorkflow(cfg.Agent, plan.Workflow, plan.Inputs)
		if err != nil {
			res.Err = err
			return res
		}
		task.WriteString("WORKFLOW: " + rendered.Name + "\n")
		for _, step := range rendered.Steps {
			switch step.Kind {
			case StepUserMessage:
				task.WriteString("TASK:\n" + step.Body + "\n\n")
			case StepWaitForAssistant:
				task.WriteString("BARRIER: complete the preceding task before continuing.\n\n")
			}
		}
	}
	rawTask := strings.TrimSpace(task.String())
	managedTask, _ := redaction.Redact(rawTask)
	systemPrompt := withSystemContext(AgentSystemPrompt(cfg.Agent), WorkflowSystemContext(cfg.Cwd))
	systemPrompt = withSystemContext(systemPrompt, cfg.SystemContext)
	systemPrompt, _ = redaction.Redact(systemPrompt)

	chatID := res.ChatID
	c.maybeAddMessage(ctx, chatID, "user", rawTask)

	start := time.Now()
	skillSettings, err := c.workflowSkillSettings(ctx, cfg.Agent, plans[0].Workflow, cfg.ChatSettings)
	if err != nil {
		res.Err = err
		return res
	}
	managed, runErr := c.RunManagedAgent(ctx, ManagedRunRequest{
		FinishEvidence: plans[0].Workflow.FinishEvidence,
		SkillSettings:  &skillSettings,
		Surface:        SurfaceWorkflow, Agent: cfg.Agent, CLI: cfg.CLI, Cwd: cfg.Cwd,
		Model: cfg.Model, Local: cfg.ChatSettings.Local, Task: managedTask,
		Instructions: systemPrompt, Env: cfg.Env, ApprovalScope: chatID,
		OnEvent: func(event ManagedRunEvent) {
			c.persistManagedSkillReceipt(ctx, chatID, event)
			emitWorkflowEvent(cfg.OnEvent, managedWorkflowEvent(res.WorkflowName, event))
		},
	})
	if managed == nil {
		res.Err = runErr
		res.Outcome = managedRunOutcome(runErr)
		return res
	}
	res.RunID = managed.ID
	final := redaction.Reveal(managed.Final)
	reply := &Reply{Text: final, SessionID: managed.SessionID}
	turn := TurnResult{
		Index: turnIndex, WorkflowName: res.WorkflowName, UserMsg: rawTask,
		Reply: reply, DurationMs: time.Since(start).Milliseconds(),
	}
	res.Turns = append(res.Turns, turn)
	res.SessionID = managed.SessionID
	c.maybeAddMessageWithMeta(ctx, chatID, "assistant", final, map[string]any{
		"managed_run_id": managed.ID, "managed_state": managed.State,
		"artifacts": managed.Artifacts, "working_memory_items": len(managed.Memory),
	})
	if cfg.OnTurn != nil {
		cfg.OnTurn(turn)
	}
	if runErr != nil {
		res.Err = runErr
		res.Outcome = managedRunOutcome(runErr)
	} else {
		res.Outcome = OutcomeCompleted
	}
	return res
}

func managedWorkflowEvent(workflowName string, event ManagedRunEvent) WorkflowRunEvent {
	out := WorkflowRunEvent{
		WorkflowName: workflowName, TurnIndex: max(event.Turn-1, 0),
		Type: event.Type, Tool: event.Tool, Detail: event.Detail, OK: event.OK,
	}
	switch event.Type {
	case "run.started":
		out.Type = "workflow_start"
	case "run.finished":
		out.Type = "workflow_finish"
	case "turn.started":
		out.Type = "turn_start"
	case "turn.finished":
		out.Type = "turn_finish"
	case "tool.requested":
		out.Type = "tool_start"
	case "tool.finished", "tool.denied":
		out.Type = "tool_end"
	case "protocol.invalid":
		out.Type = "error"
	case "model.tool_start":
		out.Type = "tool_start"
	case "model.tool_end":
		out.Type = "tool_end"
	case "model.reasoning", "model.step_start", "model.step_finish":
		out.Type = strings.TrimPrefix(event.Type, "model.")
	}
	return out
}

func managedRunOutcome(err error) RunOutcome {
	if errors.Is(err, context.Canceled) {
		return OutcomeCancelled
	}
	if err != nil {
		return OutcomeAgentFailed
	}
	return OutcomeCompleted
}

func (c *Core) maybeAddMessageWithMeta(ctx context.Context, chatID, role, content string, meta map[string]any) {
	if chatID == "" {
		return
	}
	_, _ = c.AddMessage(ctx, chatID, role, content, meta)
}
