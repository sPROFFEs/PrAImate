package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (c *Core) continueManagedChat(
	ctx context.Context,
	chat *Chat,
	agent *Agent,
	surface ExecutionSurface,
	task, userMessage, cwd, systemPrompt string,
	redaction *PrivacyRedaction,
	emit StreamHandler,
	activity *[]map[string]any,
) (*ChatTurn, error) {
	start := time.Now()
	managed, runErr := c.RunManagedAgent(ctx, ManagedRunRequest{
		TaskBudgetID:  "chat:" + chat.ID,
		SkillSettings: &chat.Settings,
		Surface:       surface, Agent: agent, CLI: chat.CLIAgent, Cwd: cwd,
		Model: chat.Settings.Model, Local: chat.Settings.Local,
		Task: task, Instructions: systemPrompt, ApprovalScope: chat.ID,
		OnEvent: func(event ManagedRunEvent) {
			c.persistManagedSkillReceipt(ctx, chat.ID, event)
			if emit != nil {
				emit(managedStreamEvent(event))
			}
		},
	})
	if managed == nil {
		return nil, runErr
	}
	final := redaction.Reveal(managed.Final)
	meta := map[string]any{
		"managed_run_id":       managed.ID,
		"managed_state":        managed.State,
		"artifacts":            managed.Artifacts,
		"working_memory_items": len(managed.Memory),
	}
	if activity != nil && len(*activity) > 0 {
		meta["activity"] = *activity
	}
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			meta["interrupted"] = true
			if final == "" {
				final = "Managed run stopped before producing a final response."
			}
			persistCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := c.AddMessage(persistCtx, chat.ID, "assistant", final, meta); err != nil {
				return nil, fmt.Errorf("persist stopped managed run: %w", err)
			}
			return &ChatTurn{UserMessage: userMessage, Reply: final, SessionID: managed.SessionID, ManagedRunID: managed.ID, DurationMs: time.Since(start).Milliseconds()}, nil
		}
		return nil, runErr
	}
	if emit != nil && final != "" {
		emit(StreamEvent{Type: "text", Text: final, OK: true})
	}
	if _, err := c.AddMessage(ctx, chat.ID, "assistant", final, meta); err != nil {
		return nil, fmt.Errorf("persist managed reply: %w", err)
	}
	return &ChatTurn{
		UserMessage: userMessage, Reply: final, SessionID: managed.SessionID, ManagedRunID: managed.ID,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

// Only the host model transport creates this typed receipt. Model text and
// tool claims cannot mark skills as delivered.
func (c *Core) persistManagedSkillReceipt(ctx context.Context, chatID string, event ManagedRunEvent) {
	if chatID == "" || event.Type != "skills.delivered" || !event.OK {
		return
	}
	state, ok := event.Payload["receipt"].(*SkillRuntimeState)
	if !ok || state == nil || state.Status != "delivered" {
		return
	}
	_ = c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) { s.SkillRuntime = state })
}

// managedChatTask rebuilds bounded on-chat context from the encrypted message
// store. Each user turn is a separate managed run, so this preserves normal
// chat continuity without turning per-run working memory into cross-chat
// memory or depending on a provider-specific native resume format.
func (c *Core) managedChatTask(ctx context.Context, chatID string, attachments []string) (string, error) {
	messages, err := c.ListMessages(ctx, chatID, 0)
	if err != nil {
		return "", fmt.Errorf("load managed chat history: %w", err)
	}
	const maxHistoryChars = 32_000
	selected := make([]string, 0, len(messages))
	used := 0
	eligible := 0
	for i := range messages {
		if messages[i].Role == "user" || messages[i].Role == "assistant" {
			eligible++
		}
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" && messages[i].Role != "assistant" {
			continue
		}
		entry := strings.ToUpper(messages[i].Role) + ":\n" + strings.TrimSpace(messages[i].Content) + "\n"
		if i == len(messages)-1 && messages[i].Role == "user" && len(attachments) > 0 {
			entry += "ATTACHED FILES (read-only access through the underlying CLI):\n"
			for _, path := range attachments {
				entry += "- " + path + "\n"
			}
		}
		if used+len(entry) > maxHistoryChars {
			break
		}
		selected = append(selected, entry)
		used += len(entry)
	}
	var task strings.Builder
	task.WriteString("Continue this conversation. Answer the latest USER message.\n\n")
	if len(selected) < eligible {
		task.WriteString("… older chat messages omitted …\n\n")
	}
	for i := len(selected) - 1; i >= 0; i-- {
		task.WriteString(selected[i])
		task.WriteByte('\n')
	}
	return strings.TrimSpace(task.String()), nil
}

func managedStreamEvent(event ManagedRunEvent) StreamEvent {
	out := StreamEvent{Type: event.Type, Tool: event.Tool, Detail: event.Detail, OK: event.OK}
	switch event.Type {
	case "run.started", "agent.started", "turn.started":
		out.Type = "step_start"
	case "turn.finished", "agent.finished", "run.finished":
		out.Type = "step_finish"
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
