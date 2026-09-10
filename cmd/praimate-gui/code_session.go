package main

// Code sessions are live PTY terminals (StartTerminal) — the real CLI.
// While a PTY is alive, its bounded output history is replayed when the Code
// page is reopened. We also persist a lightweight chat record tagged
// surface="code" (folder + cli + model + optional local route), so the Chats
// tab can reattach to the original PTY or launch a replacement after it has
// exited. RecordCodeSession is called once at launch, so replacements do not
// create duplicate session rows.

import (
	"fmt"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

type StartedCodeSession struct {
	TermID string `json:"termId"`
	ChatID string `json:"chatId"`
}

// StartCodeSessionWithSkills freezes the new chat and its optional skill
// choices before launching the native CLI. A failed launch removes the unused
// chat row, so the Sessions list is not polluted by half-created terminals.
func (a *App) StartCodeSessionWithSkills(agentID, cli, model, cwd, localEndpoint, localModel, choices string) (StartedCodeSession, error) {
	var out StartedCodeSession
	chatID, err := a.RecordCodeSession(agentID, cli, model, cwd, localEndpoint, "", localModel)
	if err != nil {
		return out, err
	}
	c, err := a.requireCore()
	if err != nil {
		return out, err
	}
	fail := func(cause error) (StartedCodeSession, error) {
		_ = c.DeleteChat(a.ctx, chatID)
		return StartedCodeSession{}, cause
	}
	if choices != "" {
		if _, err := a.SaveChatSkillChoicesV2(chatID, choices); err != nil {
			return fail(err)
		}
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return fail(err)
	}
	termID, err := a.startTerminal(agentID, cli, model, cwd, localEndpoint, localModel, false, nil, &chat.Settings)
	if err != nil {
		return fail(err)
	}
	if err := a.BindChatToTerminal(termID, chatID); err != nil {
		a.CloseTerminal(termID)
		return fail(err)
	}
	return StartedCodeSession{TermID: termID, ChatID: chatID}, nil
}

// StartTerminalForChat starts a replacement PTY from the persisted chat truth.
// In particular, it consumes the chat's frozen, versioned skill selection so
// edits made in the session settings govern the next native CLI process.
func (a *App) StartTerminalForChat(chatID string, resume bool) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return "", err
	}
	var endpoint, localModel string
	if chat.Settings.Local != nil {
		endpoint = chat.Settings.Local.Endpoint
		localModel = chat.Settings.Local.Model
	}
	termID, err := a.startTerminal(
		chat.AgentID,
		chat.CLIAgent,
		chat.Settings.Model,
		chat.WorkspacePath,
		endpoint,
		localModel,
		resume,
		nil,
		&chat.Settings,
	)
	if err != nil {
		return "", err
	}
	if err := a.BindChatToTerminal(termID, chatID); err != nil {
		a.CloseTerminal(termID)
		return "", err
	}
	return termID, nil
}

// RecordCodeSession persists a surface="code" chat pointer for a freshly
// launched terminal session and returns its id. agentID preserves the persona
// used to launch the PTY so every reopening surface can identify and restore
// it. localEndpoint, when set, is stored so reopening restores the local route.
func (a *App) RecordCodeSession(agentID, cli, model, cwd, localEndpoint, _ string, localModel string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cwd) == "" {
		return "", fmt.Errorf("a project folder is required")
	}
	if cli == "" {
		cli = "claude"
	}
	startModel := model
	if localEndpoint != "" {
		startModel = "" // the local route carries the model
	}
	var chat *core.Chat
	if strings.TrimSpace(agentID) != "" {
		chat, err = c.StartInteractiveChat(a.ctx, agentID, cli, cwd)
	} else {
		chat, err = c.StartCleanChat(a.ctx, cli, startModel, cwd)
	}
	if err != nil {
		return "", err
	}
	_ = c.UpdateChatSettings(a.ctx, chat.ID, func(s *core.ChatSettings) {
		s.Surface = "code"
		if localEndpoint != "" {
			s.Local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
		} else if model != "" {
			s.Model = model
		}
	})
	return chat.ID, nil
}

// BindChatToTerminal pairs a live PTY with its chat row so the Sessions
// panel can resume the running terminal instead of starting a fresh
// duplicate. Called by Code.svelte right after RecordCodeSession.
func (a *App) BindChatToTerminal(termID, chatID string) error {
	if a.terms == nil || termID == "" || chatID == "" {
		return fmt.Errorf("terminal id and chat id are required")
	}
	return a.terms.bindChat(termID, chatID)
}

// GetCodeSessionSnapshot returns the bounded, in-memory output tail for a
// live Code terminal. No terminal output is persisted to disk.
func (a *App) GetCodeSessionSnapshot(chatID, termID string) (TerminalSnapshot, error) {
	if a.terms == nil {
		return TerminalSnapshot{}, fmt.Errorf("terminal manager is not available")
	}
	return a.terms.codeSnapshot(chatID, termID)
}
