package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func TestMCPSkillsShimProtocol(t *testing.T) {
	fetch := func(path string, q map[string]string) (string, error) {
		switch path {
		case "/list":
			return `[{"name":"forge-review","ref":"local/forge-review","description":"Review code"}]`, nil
		case "/load":
			return "FORGE REVIEW INSTRUCTIONS BODY", nil
		case "/resource":
			return "CHECKLIST CONTENT", nil
		}
		return "", nil
	}

	// 1. Test initialize
	var out bytes.Buffer
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n")
	code := serveMCPSkills(in, &out, fetch)
	if code != 0 {
		t.Fatalf("initialize exit code = %d", code)
	}
	var initResp map[string]any
	if err := json.Unmarshal(out.Bytes(), &initResp); err != nil {
		t.Fatalf("unmarshal init response: %v, raw=%s", err, out.String())
	}
	res, _ := initResp["result"].(map[string]any)
	serverInfo, _ := res["serverInfo"].(map[string]any)
	if serverInfo["name"] != "praimate-skills" {
		t.Fatalf("expected praimate-skills, got %+v", serverInfo)
	}

	// 2. Test tools/list
	out.Reset()
	in = strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n")
	code = serveMCPSkills(in, &out, fetch)
	if code != 0 {
		t.Fatalf("tools/list exit code = %d", code)
	}
	var listResp map[string]any
	if err := json.Unmarshal(out.Bytes(), &listResp); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	tools, _ := listResp["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// 3. Test tools/call load_skill
	out.Reset()
	in = strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"load_skill","arguments":{"name":"forge-review"}}}` + "\n")
	code = serveMCPSkills(in, &out, fetch)
	if code != 0 {
		t.Fatalf("tools/call load_skill exit code = %d", code)
	}
	var callResp map[string]any
	if err := json.Unmarshal(out.Bytes(), &callResp); err != nil {
		t.Fatalf("unmarshal call response: %v", err)
	}
	content, _ := callResp["result"].(map[string]any)["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("expected text content, got %+v", callResp)
	}
	text, _ := content[0].(map[string]any)["text"].(string)
	if !strings.Contains(text, "FORGE REVIEW INSTRUCTIONS BODY") {
		t.Fatalf("unexpected content: %q", text)
	}
}

func TestSkillsBrokerIsolatedFromUserMCP(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	app := &App{ctx: context.Background(), core: c}

	broker, err := app.ensureSkillsBroker()
	if err != nil {
		t.Fatalf("ensure skills broker: %v", err)
	}
	if broker == nil || broker.addr == "" || broker.token == "" {
		t.Fatalf("invalid broker: %+v", broker)
	}

	// Verify user-facing MCPServers() does NOT include the internal skills broker
	userMCPs, err := app.MCPServers()
	if err != nil {
		t.Fatal(err)
	}
	for _, mcp := range userMCPs {
		if mcp.ID == "praimate_skills" || strings.Contains(mcp.Name, "Skills") {
			t.Fatalf("internal skills broker leaked into user MCP catalog: %+v", mcp)
		}
	}
}
