package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedProjectBrokerContainsPathsAndRequiresWriteApproval(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "safe.txt"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	approved := false
	broker, err := newManagedToolBroker(context.Background(), nil, AgentCapabilities{ReadProject: true, ModifyFiles: true}, root, &ApprovalConfig{
		Request: func(_ context.Context, tool string, _ map[string]any) (bool, error) {
			return approved && tool == "project.write", nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	if _, err := broker.ExecuteTool(context.Background(), "project.read", []byte(`{"path":"escape/secret.txt"}`)); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink escape err = %v", err)
	}
	if _, err := broker.ExecuteTool(context.Background(), "project.write", []byte(`{"path":"new.txt","content":"new"}`)); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("denied write err = %v", err)
	}
	approved = true
	if _, err := broker.ExecuteTool(context.Background(), "project.write", []byte(`{"path":"new.txt","content":"new"}`)); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "new.txt")); err != nil || string(body) != "new" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestManagedCommandBrokerUsesArgvAndApproval(t *testing.T) {
	root := t.TempDir()
	var input map[string]any
	broker, err := newManagedToolBroker(context.Background(), nil, AgentCapabilities{ExecuteCommands: true}, root, &ApprovalConfig{
		Request: func(_ context.Context, _ string, got map[string]any) (bool, error) { input = got; return true, nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	out, err := broker.ExecuteTool(context.Background(), "command.run", []byte(`{"command":"go","args":["version"],"timeout_seconds":5}`))
	if err != nil || !strings.Contains(out, "go version") || input["command"] != "go" {
		t.Fatalf("out=%q input=%#v err=%v", out, input, err)
	}
}

func TestManagedKnowledgeToolsMatrixAndSearch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("PRAIMATE_HOME", home)

	// Set up agent knowledge
	agentID := "test-agent"
	kDir, err := AgentKnowledgeDir(agentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(kDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kDir, "docs", "auth.md"), []byte("JWT_REFRESH_INTERVAL = 900\nsecret token configuration"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create graphify-out that should be excluded by knowledge.search
	gDir := filepath.Join(kDir, "graphify-out")
	if err := os.MkdirAll(gDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gDir, "graph.json"), []byte(`{"nodes":[], "JWT_REFRESH_INTERVAL": "polluted"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. None mode
	agentNone := &Agent{ID: agentID, Knowledge: ""}
	brokerNone, err := newManagedToolBroker(context.Background(), agentNone, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer brokerNone.Close()
	if _, err := brokerNone.ExecuteTool(context.Background(), "knowledge.read", []byte(`{"path":"docs/auth.md"}`)); err == nil {
		t.Fatal("expected knowledge.read denied in none mode")
	}
	if _, err := brokerNone.ExecuteTool(context.Background(), "knowledge.search", []byte(`{"query":"JWT"}`)); err == nil {
		t.Fatal("expected knowledge.search denied in none mode")
	}
	if _, err := brokerNone.ExecuteTool(context.Background(), "knowledge.query", []byte(`{"question":"JWT"}`)); err == nil {
		t.Fatal("expected knowledge.query denied in none mode")
	}

	// 2. Raw mode
	agentRaw := &Agent{ID: agentID, Knowledge: "raw"}
	brokerRaw, err := newManagedToolBroker(context.Background(), agentRaw, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer brokerRaw.Close()
	readOut, err := brokerRaw.ExecuteTool(context.Background(), "knowledge.read", []byte(`{"path":"docs/auth.md"}`))
	if err != nil || !strings.Contains(readOut, "JWT_REFRESH_INTERVAL") {
		t.Fatalf("raw read failed: %v, out=%q", err, readOut)
	}
	searchOut, err := brokerRaw.ExecuteTool(context.Background(), "knowledge.search", []byte(`{"query":"JWT_REFRESH_INTERVAL"}`))
	if err != nil || !strings.Contains(searchOut, "docs/auth.md:1:JWT_REFRESH_INTERVAL") {
		t.Fatalf("raw search failed: %v, out=%q", err, searchOut)
	}
	if strings.Contains(searchOut, "graphify-out") {
		t.Fatalf("knowledge.search did not exclude graphify-out: %q", searchOut)
	}
	if _, err := brokerRaw.ExecuteTool(context.Background(), "knowledge.query", []byte(`{"question":"JWT"}`)); err == nil {
		t.Fatal("expected knowledge.query denied in raw mode")
	}

	// 3. RAG mode - supports both search, read, and query
	agentRAG := &Agent{ID: agentID, Knowledge: "rag"}
	brokerRAG, err := newManagedToolBroker(context.Background(), agentRAG, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer brokerRAG.Close()
	readOutRAG, err := brokerRAG.ExecuteTool(context.Background(), "knowledge.read", []byte(`{"path":"docs/auth.md"}`))
	if err != nil || !strings.Contains(readOutRAG, "JWT_REFRESH_INTERVAL") {
		t.Fatalf("rag read failed: %v, out=%q", err, readOutRAG)
	}
	searchOutRAG, err := brokerRAG.ExecuteTool(context.Background(), "knowledge.search", []byte(`{"query":"JWT_REFRESH_INTERVAL"}`))
	if err != nil || !strings.Contains(searchOutRAG, "docs/auth.md:1:JWT_REFRESH_INTERVAL") {
		t.Fatalf("rag search failed: %v, out=%q", err, searchOutRAG)
	}
	if strings.Contains(searchOutRAG, "graphify-out") {
		t.Fatalf("knowledge.search did not exclude graphify-out in rag mode: %q", searchOutRAG)
	}

	// Instructions verification
	inst := brokerRAG.Instructions()
	if !strings.Contains(inst, "knowledge.read") || !strings.Contains(inst, "knowledge.search") || !strings.Contains(inst, "knowledge.query") {
		t.Fatalf("rag instructions missing tools: %q", inst)
	}
	if !strings.Contains(inst, "KNOWLEDGE USAGE") || !strings.Contains(inst, "budget\":1200") {
		t.Fatalf("rag instructions missing guidance or updated budget: %q", inst)
	}
}
