package main

// Agents tab CRUD bindings — view/edit/add agents as YAML using the
// in-app editor (the same wire format `praimate agent import/export`
// speaks). Saving re-validates through core.ParseAgentYAML, so a typo
// fails with the exact field error instead of corrupting the DB row.

import (
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// AgentYAML returns the canonical YAML for an agent, for the editor.
func (a *App) AgentYAML(id string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return "", err
	}
	raw, err := core.MarshalAgentYAML(agent)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// SaveAgentYAML validates and upserts an agent from its YAML source.
// Creates when the id is new, updates otherwise. Returns the saved
// agent so the list refreshes without a reload.
func (a *App) SaveAgentYAML(yamlBody string) (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ImportAgentYAML(a.ctx, []byte(yamlBody), "")
}

// NewAgentTemplateYAML returns a starter agent definition for the
// "+ New agent" editor, pre-filled with every supported field so the
// user edits rather than remembers the schema.
func (a *App) NewAgentTemplateYAML() string {
	return strings.TrimLeft(`
schema: praimate.agent/v1
id: my-agent
name: My Agent
description: One line about what this agent is for.
icon: robot

instructions: |
  You are a helpful assistant. Describe the persona, the rules and the
  output style here — this becomes the system prompt for every chat.

# Which wrapped CLIs may run this agent.
supports:
  - claude
  - codex
  - opencode

# Where the agent can be launched from in the GUI. Remove entries to
# restrict it; delete the whole list to allow everywhere.
surfaces:
  - chat
  - terminal
  - editor

# Optional knowledge base: "raw" (a folder of documents the agent reads
# directly) or "rag" (the same folder indexed with graphify for
# retrieval). After saving, attach the documents in the Knowledge panel
# below the editor — they ship inside the exported .praimate-agent pack.
# knowledge: raw

# Optional: MCP servers (by id from the MCP tab) and scripted workflows.
mcp_servers: []
workflows: []

# Optional environment setup script. Attach it from the Agent Studio after
# saving; it travels inside .praimate-agent packs and recipients must click
# "Run requirements script" explicitly after import.
# requirements:
#   os: linux # linux or windows
#   script: setup.sh
#   instructions: Install Docker before continuing.
`, "\n")
}
