package core

// Interactive chat — send a follow-up message into an existing chat and
// get the assistant's reply, resuming the underlying CLI session so the
// conversation has continuity.
//
// This is the conversational counterpart to RunWorkflow's scripted
// turns. Each call is one round-trip: persist the user message, drive
// the CLI (Resume if we have a session id, else SingleShot), persist
// the reply, and remember the new session id on the chat row.
//
// Privacy redaction applies on the way out and is revealed on the way
// back, matching RunWorkflow.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"os"
	"strings"
	"time"
)

// ChatTurn is one completed interactive exchange.
type ChatTurn struct {
	UserMessage  string
	Reply        string
	SessionID    string
	ManagedRunID string
	DurationMs   int64
}

// SetChatSessionID persists the adapter session id on a chat row so the
// next ContinueChat / interactive resume picks up where this one left
// off. Exposed so the workflow runner can stamp it after a persisted
// run, making any persisted chat continuable.
func (c *Core) SetChatSessionID(ctx context.Context, chatID, sessionID string) error {
	if c.store == nil {
		return errors.New("SetChatSessionID: no store configured")
	}
	_, err := c.store.DB().ExecContext(ctx,
		`UPDATE chats SET session_id = ? WHERE id = ?`, sessionID, chatID)
	return err
}

// UpdateChatSettings mutates a chat's persisted settings through fn and
// writes them back. Used by the GUI's per-chat model/tools controls.
func (c *Core) UpdateChatSettings(ctx context.Context, chatID string, fn func(*ChatSettings)) error {
	if c.store == nil {
		return errors.New("UpdateChatSettings: no store configured")
	}
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	previous, _ := json.Marshal([]any{chat.Settings.SkillsV2, chat.Settings.SkillsLock})
	hadV2 := chat.Settings.SkillsV2 != nil
	fn(&chat.Settings)
	next, _ := json.Marshal([]any{chat.Settings.SkillsV2, chat.Settings.SkillsLock})
	if chat.SessionID != "" && (hadV2 || chat.Settings.SkillsV2 != nil) && string(previous) != string(next) {
		return errors.New("context_resync_required: change v2 skills in a fresh session; native context cannot be revoked here")
	}
	if err := validateChatSkills(chat.Settings); err != nil {
		return err
	}
	if err := c.retainSkillSelections(ctx, "chat:"+chatID, []skills.SkillScope{chatSkillScope(chat.Settings)}); err != nil {
		return err
	}
	raw, err := json.Marshal(chat.Settings)
	if err != nil {
		return fmt.Errorf("UpdateChatSettings: marshal: %w", err)
	}
	_, err = c.store.DB().ExecContext(ctx,
		`UPDATE chats SET settings_json = ?, updated_at = ? WHERE id = ?`,
		string(raw), time.Now().UTC().Format(time.RFC3339Nano), chatID)
	return err
}

// UpdateChatConfig reconfigures an existing chat: the CLI behind it,
// the pinned model and the Tools level — the GUI counterpart of the
// TUI's per-chat settings sheet. Switching the CLI clears the stored
// session id (a session belongs to the CLI that created it; the next
// turn starts a fresh session on the new CLI with full history still
// in the DB). Empty cli keeps the current one.
// SearchChats finds chats whose title OR message content matches the
// query (case-insensitive substring). Newest first.
func (c *Core) SearchChats(ctx context.Context, query string, limit int) ([]Chat, error) {
	if c.store == nil {
		return nil, errors.New("SearchChats: no store configured")
	}
	if limit <= 0 {
		limit = 50
	}
	like := "%" + strings.ToLower(strings.TrimSpace(query)) + "%"
	rows, err := c.store.DB().QueryContext(ctx, `
		SELECT `+chatColumns+` FROM chats
		WHERE lower(title) LIKE ?
		   OR id IN (SELECT chat_id FROM messages WHERE lower(content) LIKE ?)
		ORDER BY updated_at DESC LIMIT ?`, like, like, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		ch, err := scanChat(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, rows.Err()
}

func (c *Core) UpdateChatConfig(ctx context.Context, chatID, cli, model, tools string) error {
	if c.store == nil {
		return errors.New("UpdateChatConfig: no store configured")
	}
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if cli != "" && cli != chat.CLIAgent {
		if _, err := GetCLIAdapter(cli); err != nil {
			return err
		}
		if _, err := c.store.DB().ExecContext(ctx,
			`UPDATE chats SET cli_agent = ?, session_id = NULL WHERE id = ?`, cli, chatID); err != nil {
			return fmt.Errorf("UpdateChatConfig: switch cli: %w", err)
		}
	}
	return c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) {
		s.Model = model
		s.Tools = tools
		s.ToolsConfigured = true
	})
}

// ContinueChat sends userMessage to the chat's CLI and returns the
// reply. cwd is the working directory the CLI runs in (use the chat's
// workspace_path, or the app cwd). The first call on a fresh chat
// starts a new session; subsequent calls resume it.
//
// systemPrompt is sent only on the FIRST turn of a brand-new session
// (when the chat has no session id yet) so the agent's instructions
// frame the conversation without being repeated every turn.
func (c *Core) ContinueChat(ctx context.Context, chatID, userMessage, cwd, systemPrompt string) (*ChatTurn, error) {
	return c.ContinueChatWithAttachments(ctx, chatID, userMessage, cwd, systemPrompt, nil)
}

// ContinueChatWithAttachments is ContinueChat plus file ingestion:
// attachments is a list of absolute paths (images, PDFs, docs, …) the
// user attached to this turn. The paths are appended to the outbound
// message so file-tool-capable CLIs (claude, codex, gemini, opencode)
// read them from disk; the stored user message records them in Meta so
// the GUI can render previews.
func (c *Core) ContinueChatWithAttachments(ctx context.Context, chatID, userMessage, cwd, systemPrompt string, attachments []string) (*ChatTurn, error) {
	return c.ContinueChatStream(ctx, chatID, userMessage, cwd, systemPrompt, attachments, nil)
}

// ContinueChatStream is the live variant: when onEvent is non-nil and
// the chat's CLI supports streaming (claude/openclaude stream-json,
// codex exec --json), text deltas and tool-call activity are emitted as
// they happen and the turn can be interrupted by cancelling ctx — an
// interrupted turn persists whatever text already streamed (flagged
// interrupted in the message meta) instead of erroring. CLIs without a
// stream fall back to the buffered path; onEvent then just never fires
// before the final reply.
//
// Note on privacy: streamed deltas show the REDACTED outbound form;
// placeholder reveal happens only on the persisted final message (same
// trade Claude/Codex desktop make — you watch the wire format live).
func (c *Core) ContinueChatStream(ctx context.Context, chatID, userMessage, cwd, systemPrompt string, attachments []string, onEvent StreamHandler) (*ChatTurn, error) {
	if c.store == nil {
		return nil, errors.New("ContinueChat: no store configured")
	}
	if userMessage == "" && len(attachments) == 0 {
		return nil, errors.New("ContinueChat: empty message")
	}
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	var agent *Agent
	managedChat := false
	if chat.AgentID != "" {
		agent, _ = c.GetAgent(ctx, chat.AgentID)
		if agent != nil {
			runtimeConfig, runtimeErr := c.ResolveEffectiveAgentConfig(ctx, agent)
			if runtimeErr != nil {
				return nil, runtimeErr
			}
			managedChat = runtimeConfig.Mode == RuntimeAgentic && chat.Settings.Surface != "agent-helper"
		}
	}
	var skillPayload string
	var skillState *SkillRuntimeState
	if !managedChat {
		skillPayload, skillState, err = c.BuildChatSkillPayload(ctx, chat.Settings, systemPrompt+"\n"+userMessage)
		if err != nil {
			return nil, err
		}
	}
	if skillState != nil {
		if err := c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) { s.SkillRuntime = skillState }); err != nil {
			return nil, err
		}
		chat.Settings.SkillRuntime = skillState
	}
	adapter, err := GetCLIAdapter(chat.CLIAgent)
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		cwd = chat.WorkspacePath
	}
	if cwd == "" || cwd == "." {
		home, err := os.UserHomeDir()
		if err == nil {
			cwd = home
		} else {
			cwd = "."
		}
	}

	// Redact outbound; the stored user message keeps the original text.
	privacy := c.PrivacyScanner()
	outbound, matches := privacy.Redact(userMessage)
	if len(attachments) > 0 {
		var b strings.Builder
		b.WriteString(outbound)
		b.WriteString("\n\nThe user attached these files — read them from disk with your file tools:\n")
		for _, p := range attachments {
			b.WriteString("- " + p + "\n")
		}
		outbound = b.String()
	}
	// The first controlled payload is placed in the adapter's observable system
	// field. Resume APIs do not expose that field, so a new controlled payload is
	// explicitly prepended to the resumed user request rather than claiming the
	// private native session still contains a resident block.
	if skillPayload != "" && chat.SessionID != "" && adapter.SupportsResume() {
		outbound = skillPayload + "\n\n" + outbound
	}

	var meta map[string]any
	if len(attachments) > 0 {
		meta = map[string]any{"attachments": attachments}
	}
	if _, err := c.AddMessage(ctx, chatID, "user", userMessage, meta); err != nil {
		return nil, fmt.Errorf("persist user message: %w", err)
	}

	// Wrap the caller's handler to also collect a compact activity log
	// that persists on the assistant message, so reopened chats still
	// show what the agent did.
	var activity []map[string]any
	collect := func(ev StreamEvent) {
		if len(activity) < 100 {
			switch ev.Type {
			case "reasoning":
				if text := compactActivityText(ev.Text, 1200); text != "" {
					activity = append(activity, map[string]any{"type": "reasoning", "text": text})
				}
			case "step_start":
				activity = append(activity, map[string]any{"type": "step_start", "detail": compactActivityText(ev.Detail, 300), "ok": true})
			case "step_finish":
				activity = append(activity, map[string]any{"type": "step_finish", "detail": compactActivityText(ev.Detail, 300), "ok": ev.OK})
			case "error":
				activity = append(activity, map[string]any{"type": "error", "detail": compactActivityText(ev.Detail, 500), "ok": false})
			case "tool_start":
				activity = append(activity, map[string]any{"type": "tool", "tool": ev.Tool, "detail": compactActivityText(ev.Detail, 300), "ok": true})
			}
		}
		if ev.Type == "tool_end" && !ev.OK {
			for i := len(activity) - 1; i >= 0; i-- {
				if activity[i]["ok"] == true && (activity[i]["type"] == "tool" || activity[i]["tool"] != nil) {
					activity[i]["ok"] = false
					break
				}
			}
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}

	// Ask level: wire the approval shim when a provider is registered.
	var approval *ApprovalConfig
	if chat.Settings.Tools == "ask" && c.approvalProvider != nil {
		approval = c.approvalProvider(chatID)
	}
	surface := SurfaceChat
	if chat.Settings.Surface == "studio" || chat.Settings.Surface == "agent-helper" {
		surface = SurfaceStudio
	}
	if agent != nil {
		if !contains(agent.Supports, chat.CLIAgent) {
			return nil, fmt.Errorf("agent %q does not support CLI %q", agent.ID, chat.CLIAgent)
		}
		// Agent Studio's authoring helper edits the definition itself and must
		// remain on the native authoring path. A normal document-studio chat
		// still uses the managed runtime.
		if managedChat {
			managedTask, historyErr := c.managedChatTask(ctx, chat.ID, attachments)
			if historyErr != nil {
				return nil, historyErr
			}
			redaction := privacy.NewRedactionSession()
			managedTask, _ = redaction.Redact(managedTask)
			managedSystem, _ := redaction.Redact(systemPrompt)
			return c.continueManagedChat(ctx, chat, agent, surface, managedTask, userMessage,
				cwd, managedSystem, redaction, collect, &activity)
		}
	}
	executionAgent := agent
	if chat.Settings.Surface == "agent-helper" {
		// The built-in authoring assistant is deliberately native even if its
		// manifest is edited to an agentic preset. Its job requires the CLI's
		// authoring permissions, which the managed runtime correctly denies.
		executionAgent = nil
	}
	effective, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{
		Surface: surface, Agent: executionAgent, ChatID: chatID, CLI: chat.CLIAgent,
		Cwd: cwd, Model: chat.Settings.Model, Tools: chat.Settings.Tools,
		ToolsConfigured: chat.Settings.ToolsConfigured,
		Local:           chat.Settings.Local, MCPServers: chat.Settings.MCPServers,
		ExplicitMCP: chat.Settings.MCPConfigured || len(chat.Settings.MCPServers) > 0,
		Approval:    approval,
	})
	if err != nil {
		return nil, err
	}
	if err := c.PrepareExecution(ctx, effective); err != nil {
		return nil, err
	}
	resumeOpts := ResumeOpts{Message: outbound, Cwd: cwd, Model: effective.Model, Tools: effective.Tools, Approval: effective.Approval, Env: effective.Env}
	shotOpts := SingleShotOpts{
		Cwd:          cwd,
		Message:      outbound,
		SystemPrompt: privacyRedactPlain(privacy, withSystemContext(systemPrompt, skillPayload)),
		Model:        effective.Model,
		Tools:        effective.Tools,
		Approval:     effective.Approval,
		Env:          effective.Env,
	}
	resuming := chat.SessionID != "" && adapter.SupportsResume()
	requestSystem := shotOpts.SystemPrompt
	if resuming {
		requestSystem = "" // Native/private system context is outside our coverage.
	}
	if err := validateControlledSkillRequest(chat.Settings, requestSystem, outbound); err != nil {
		return nil, err
	}
	if chat.Settings.SkillsV2 != nil {
		if err := c.reserveSkillTaskInput(ctx, skillTaskBudgetID(ctx, "chat:"+chat.ID), int64(len(requestSystem)+len(outbound)), int64(len(skillPayload)), 8<<20); err != nil {
			return nil, err
		}
	}

	start := time.Now()
	var reply *Reply
	streamed := false
	shouldStream := onEvent != nil || isOpenCodeLikeAdapter(adapter.Name())
	if sc, ok := adapter.(streamingAdapter); ok && shouldStream {
		if resuming {
			reply, err = sc.ResumeStream(ctx, chat.SessionID, resumeOpts, collect)
		} else {
			reply, err = sc.SingleShotStream(ctx, shotOpts, collect)
		}
		if errors.Is(err, ErrStreamUnsupported) {
			reply, err = nil, nil
		} else {
			streamed = true
		}
	}
	if !streamed {
		if resuming {
			reply, err = adapter.Resume(ctx, chat.SessionID, resumeOpts)
		} else {
			reply, err = adapter.SingleShot(ctx, shotOpts)
		}
	}

	var assistantMeta map[string]any
	if len(activity) > 0 {
		assistantMeta = map[string]any{"activity": activity}
	}

	// If the turn errored, was interrupted, or exited non-zero, keep whatever streamed
	// instead of losing the partial reply. That's the desktop-app contract.
	if (err != nil || (reply != nil && reply.ExitCode != 0)) && reply != nil && reply.Text != "" {
		if assistantMeta == nil {
			assistantMeta = map[string]any{}
		}
		if err != nil && errors.Is(err, context.Canceled) {
			assistantMeta["interrupted"] = true
		} else {
			if err != nil {
				assistantMeta["error"] = err.Error()
			} else {
				assistantMeta["error"] = fmt.Sprintf("exited with code %d", reply.ExitCode)
			}
		}
		revealed := privacy.Reveal(reply.Text, matches)
		// ctx might be dead — persist with a fresh background context.
		pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, perr := c.AddMessage(pctx, chatID, "assistant", revealed, assistantMeta); perr != nil {
			if err != nil {
				return nil, fmt.Errorf("persist failed reply: %w (original error: %v)", perr, err)
			}
			return nil, fmt.Errorf("persist failed reply: %w", perr)
		}

		// If it's a cancellation, we return success so the UI doesn't show a red error banner.
		if err != nil && errors.Is(err, context.Canceled) {
			return &ChatTurn{
				UserMessage: userMessage,
				Reply:       revealed,
				SessionID:   reply.SessionID,
				DurationMs:  time.Since(start).Milliseconds(),
			}, nil
		}

		// Otherwise, we still return the error so the UI shows the banner,
		// but the partial text is safely in the database!
		if err != nil {
			return nil, fmt.Errorf("%s: %w", adapter.Name(), err)
		}
		return nil, fmt.Errorf("%s exited with code %d", adapter.Name(), reply.ExitCode)
	}

	if err != nil {
		return nil, fmt.Errorf("%s: %w", adapter.Name(), err)
	}
	if reply.ExitCode != 0 {
		return nil, fmt.Errorf("%s exited with code %d", adapter.Name(), reply.ExitCode)
	}

	revealed := privacy.Reveal(reply.Text, matches)
	if _, err := c.AddMessage(ctx, chatID, "assistant", revealed, assistantMeta); err != nil {
		return nil, fmt.Errorf("persist reply: %w", err)
	}
	if reply.SessionID != "" && reply.SessionID != chat.SessionID {
		_ = c.SetChatSessionID(ctx, chatID, reply.SessionID)
	}
	if skillState != nil {
		skillState.Status = "delivered"
		_ = c.UpdateChatSettings(ctx, chatID, func(s *ChatSettings) { s.SkillRuntime = skillState })
	}

	return &ChatTurn{
		UserMessage: userMessage,
		Reply:       revealed,
		SessionID:   reply.SessionID,
		DurationMs:  time.Since(start).Milliseconds(),
	}, nil
}

func (c *Core) localLLMAPIKey(ctx context.Context) (string, error) {
	raw, err := c.GetSetting(ctx, ScopeCLI, "local_llm.api_key")
	if err != nil {
		return "", fmt.Errorf("load local LLM credential: %w", err)
	}
	if len(raw) == 0 {
		return "", nil
	}
	var apiKey string
	if err := json.Unmarshal(raw, &apiKey); err != nil {
		return "", fmt.Errorf("decode local LLM credential: %w", err)
	}
	return strings.TrimSpace(apiKey), nil
}

// privacyRedactPlain redacts text and discards the match set — used for
// the system prompt, which we never need to reveal.
func privacyRedactPlain(p *PrivacyScanner, text string) string {
	if text == "" {
		return ""
	}
	out, _ := p.Redact(text)
	return out
}

func compactActivityText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return truncate(strings.ReplaceAll(text, "\n", " ⏎ "), limit)
}

// StartCleanChat creates a DB-backed chat bound to a CLI only — no
// PrAImate agent, no system prompt. model, if non-empty, pins the CLI's
// model for every turn (see ChatSettings.Model for per-CLI semantics).
// Use this from the GUI "new chat" affordance when the user wants a
// plain conversation with the CLI's model rather than an agent persona.
func (c *Core) StartCleanChat(ctx context.Context, cli, model, cwd string) (*Chat, error) {
	if c.store == nil {
		return nil, errors.New("StartCleanChat: no store configured")
	}
	if cli == "" {
		return nil, errors.New("StartCleanChat: cli required")
	}
	if _, err := GetCLIAdapter(cli); err != nil {
		return nil, err
	}
	title := cli
	if model != "" {
		title += " · " + model
	}
	title += " · " + time.Now().Format("Jan 2 15:04")
	return c.CreateChat(ctx, CreateChatRequest{
		Title:         title,
		CLIAgent:      cli,
		WorkspacePath: cwd,
		Settings:      ChatSettings{Model: model},
	})
}

// StartInteractiveChat creates a DB-backed chat for an agent and returns
// it ready for ContinueChat. It does NOT send a first message — the
// caller drives the conversation. Use this from the GUI "new chat with
// agent" affordance.
func (c *Core) StartInteractiveChat(ctx context.Context, agentID, cli, cwd string) (*Chat, error) {
	if c.store == nil {
		return nil, errors.New("StartInteractiveChat: no store configured")
	}
	agent, err := c.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	if cli == "" {
		if len(agent.Supports) == 0 {
			return nil, fmt.Errorf("agent %q supports no CLI", agentID)
		}
		cli = agent.Supports[0]
	}
	if !contains(agent.Supports, cli) {
		return nil, fmt.Errorf("agent %q does not support CLI %q", agentID, cli)
	}
	title := agent.Name + " · " + time.Now().Format("Jan 2 15:04")
	return c.CreateChat(ctx, CreateChatRequest{
		Title:         title,
		AgentID:       agentID,
		CLIAgent:      cli,
		WorkspacePath: cwd,
		Settings: ChatSettings{
			MCPServers:    append([]string(nil), agent.MCPServers...),
			MCPConfigured: len(agent.MCPServers) > 0,
		},
	})
}
