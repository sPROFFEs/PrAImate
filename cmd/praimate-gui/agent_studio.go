package main

// Agent authoring studio — the right-pane AI assistant is an ephemeral
// "agent-helper" chat: it helps the user write/refine an agent but is NOT
// part of the agent and is deleted when the studio closes. Its CLI/model
// are freely switchable (UpdateChatConfig) and never persisted onto the
// agent. Tagged surface="agent-helper" so it stays out of the Chats list.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// helperAgentID is the built-in agent whose instructions seed the studio's
// authoring assistant — same knowledge of the PrAImate agent format the
// user is editing. If the agent is missing or doesn't support the chosen
// CLI, we fall back to a plain clean chat.
const helperAgentID = "agent-builder"

// StartAgentHelperChat opens the studio's assistant pane as a throwaway
// chat preloaded with the agent-builder system prompt. When `targetAgentID`
// is non-empty, the current DB-state YAML is mirrored to `agent.yaml` in
// the explicitly requested working directory. When no directory is supplied,
// the helper uses the target agent's data directory; it never falls back to
// the process directory from which PrAImate happened to be launched.
func (a *App) StartAgentHelperChat(cli, model, cwd, targetAgentID string) (*core.Chat, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	if cli == "" {
		cli = "claude"
	}

	cwd, err = agentHelperWorkspace(targetAgentID, cwd)
	if err != nil {
		return nil, err
	}
	if targetAgentID != "" {
		agent, getErr := c.GetAgent(a.ctx, targetAgentID)
		if getErr != nil {
			return nil, getErr
		}
		if _, writeErr := writeAgentYAMLToWorkspace(agent, cwd); writeErr != nil {
			return nil, writeErr
		}
	}

	agentID := ""
	title := cli
	if agent, err := c.GetAgent(a.ctx, helperAgentID); err == nil && contains(agent.Supports, cli) {
		agentID = agent.ID
		title = agent.Name
	}
	if model != "" {
		title += " · " + model
	}
	title += " · " + time.Now().Format("Jan 2 15:04")

	// "edits" auto-approves file edits — the helper's whole job is to
	// rewrite `./agent.yaml` and the `./knowledge/` files for the user.
	// Without this the wrapped CLI runs read-only and tells the user
	// "I can't save agent.yaml from this environment".
	tools := "edits"
	if cli == "opencode" || cli == "praimate-code" {
		// OpenCode's headless run mode has no edits-only flag. Without the
		// explicit bypass it can reject the helper's write tool and merely
		// print a YAML block instead of updating ./agent.yaml.
		tools = "full"
	}
	chat, err := c.CreateChat(a.ctx, core.CreateChatRequest{
		Title:         title,
		AgentID:       agentID,
		CLIAgent:      cli,
		WorkspacePath: cwd,
		Settings:      core.ChatSettings{Model: model, Surface: "agent-helper", Tools: tools},
	})
	if err != nil {
		return nil, err
	}
	return redactChatCredential(chat), nil
}

// WriteAgentYAMLDraftToDisk mirrors the editor's current (possibly not yet
// valid/saved) draft before a helper turn. This keeps the CLI from editing a
// stale DB snapshot when the user has typed changes in the center pane.
func (a *App) WriteAgentYAMLDraftToDisk(id, cwd, body string) error {
	path, err := agentYAMLWorkspacePath(id, cwd)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}

// SyncAgentYAMLToDisk re-renders the current DB YAML into the helper's
// working directory. Called after "Save agent" so the CLI sees the
// authoritative copy on its next turn.
func (a *App) SyncAgentYAMLToDisk(id, cwd string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return "", err
	}
	// Preserve the canonical config copy used by knowledge/RAG management,
	// but return and update the helper's visible workspace copy.
	_, _ = core.WriteAgentYAMLToDisk(agent)
	return writeAgentYAMLToWorkspace(agent, cwd)
}

// ReadAgentYAMLFromDisk returns the helper workspace's ./agent.yaml.
// Used by the studio's "Reload from disk" button so the user can pull
// the helper's edits back into the editor pane in one click.
func (a *App) ReadAgentYAMLFromDisk(id, cwd string) (string, error) {
	path, err := agentYAMLWorkspacePath(id, cwd)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func agentHelperWorkspace(agentID, cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	create := false
	if cwd == "" {
		cwd = strings.TrimSpace(editorFolder)
	}
	if cwd == "" {
		var err error
		cwd, err = core.AgentDir(strings.TrimSpace(agentID))
		if err != nil {
			return "", fmt.Errorf("resolve agent data directory: %w", err)
		}
		create = true
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", fmt.Errorf("resolve helper working directory: %w", err)
	}
	if create {
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return "", fmt.Errorf("create agent data directory: %w", err)
		}
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("open helper working directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("helper working directory is not a directory: %s", abs)
	}
	return abs, nil
}

func agentYAMLWorkspacePath(id, cwd string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("agent id is required")
	}
	workspace, err := agentHelperWorkspace(id, cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(workspace, "agent.yaml"), nil
}

func writeAgentYAMLToWorkspace(agent *core.Agent, cwd string) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("write agent.yaml: nil agent")
	}
	path, err := agentYAMLWorkspacePath(agent.ID, cwd)
	if err != nil {
		return "", err
	}
	body, err := core.MarshalAgentYAML(agent)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
