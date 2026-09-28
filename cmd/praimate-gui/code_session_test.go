package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestRecordCodeSessionPreservesAgentIdentity(t *testing.T) {
	root := t.TempDir()
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportAgentYAML(context.Background(), []byte(`
schema: praimate.agent/v1
id: dev-team
name: Dev Team
description: Reviews code
instructions: Review carefully.
supports: [claude]
tools: []
mcp_servers: []
workflows: []
surfaces: [terminal]
`), ""); err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: context.Background(), core: c}

	id, err := a.RecordCodeSession("dev-team", "claude", "", root, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := c.GetChat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if chat.AgentID != "dev-team" {
		t.Fatalf("AgentID = %q, want dev-team", chat.AgentID)
	}
	if !strings.HasPrefix(chat.Title, "Dev Team") {
		t.Fatalf("Title = %q, want agent name prefix", chat.Title)
	}
	if chat.Settings.Surface != "code" {
		t.Fatalf("surface = %q, want code", chat.Settings.Surface)
	}
}
