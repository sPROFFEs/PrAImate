package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func TestResolveExecutionConfigRejectsClaudeLocalRouting(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	if CapabilitiesForCLI("claude").LocalRouting {
		t.Fatal("Claude Code must not advertise local routing")
	}
	_, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
		Surface: SurfaceChat, CLI: "claude", Cwd: t.TempDir(),
		Local: &ChatLocalEndpoint{Endpoint: "http://127.0.0.1:11434", Model: "qwen3"},
	})
	if err == nil || !strings.Contains(err.Error(), "OpenClaude") {
		t.Fatalf("Claude local routing error = %v, want OpenClaude migration guidance", err)
	}
}

func TestResolveExecutionConfigRestoresSavedOpenClaudeTokenLimits(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	if err := launcher.SaveConfig(&launcher.Config{
		DefaultLocalEndpoint:      "https://llm.example/v1",
		DefaultLocalContextTokens: 32768,
		DefaultLocalOutputTokens:  4096,
	}); err != nil {
		t.Fatal(err)
	}
	c, _ := New(Options{Store: openTempStore(t)})
	cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
		Surface: SurfaceChat, CLI: "openclaude", Cwd: t.TempDir(),
		Local: &ChatLocalEndpoint{Endpoint: "https://llm.example/v1", Model: "qwen3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.Env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"], `"qwen3":32768`) {
		t.Fatalf("context limits = %q", cfg.Env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"])
	}
	if !strings.Contains(cfg.Env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"], `"qwen3":4096`) {
		t.Fatalf("output limits = %q", cfg.Env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"])
	}
}

func TestResolveExecutionConfigLocalRouteIsSurfaceIndependent(t *testing.T) {
	c, err := New(Options{Store: openTempStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(context.Background(), ScopeCLI, "local_llm.api_key", []byte(`"encrypted-secret"`)); err != nil {
		t.Fatal(err)
	}
	for _, surface := range []ExecutionSurface{SurfaceChat, SurfaceStudio, SurfaceWorkflow, SurfaceTerminal} {
		t.Run(string(surface), func(t *testing.T) {
			cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
				Surface: surface, CLI: "praimate-code", Cwd: t.TempDir(),
				Local: &ChatLocalEndpoint{Endpoint: "https:llm.example", Model: "qwen3"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Model != "praimate-local/qwen3" {
				t.Fatalf("model=%q", cfg.Model)
			}
			if cfg.Env["OPENAI_API_KEY"] != "encrypted-secret" || cfg.Env["OPENAI_BASE_URL"] != "https://llm.example/v1" {
				t.Fatalf("unexpected env: %#v", cfg.Env)
			}
		})
	}
}

func TestResolveExecutionConfigDegradesUnsupportedPermissionToSafe(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
		Surface: SurfaceStudio, CLI: "praimate-code", Cwd: t.TempDir(), Tools: "edits",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tools != "" {
		t.Fatalf("tools=%q, want safe", cfg.Tools)
	}
	if len(cfg.Issues) != 1 || cfg.Issues[0].Code != "tools_degraded" {
		t.Fatalf("issues=%+v", cfg.Issues)
	}
}

func TestPreflightWarnsOnlyForRemotePlaintextHTTP(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	for _, tc := range []struct {
		endpoint string
		warn     bool
	}{
		{"http://127.0.0.1:11434", false},
		{"http://localhost:11434", false},
		{"http://llm.example:11434", true},
		{"https://llm.example", false},
	} {
		cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
			Surface: SurfaceChat, CLI: "openclaude", Cwd: t.TempDir(),
			Local: &ChatLocalEndpoint{Endpoint: tc.endpoint, Model: "qwen3"},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := false
		for _, issue := range cfg.Issues {
			got = got || issue.Code == "plaintext_remote_llm"
		}
		if got != tc.warn {
			t.Fatalf("endpoint %s warning=%v, want %v", tc.endpoint, got, tc.warn)
		}
	}
}

func TestPrepareExecutionWritesCredentialFreeOpenCodeRoute(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	cwd := t.TempDir()
	cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
		Surface: SurfaceTerminal, CLI: "opencode", Cwd: cwd,
		Local: &ChatLocalEndpoint{Endpoint: "https://llm.example", Model: "qwen3", APIKey: "top-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareExecution(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) == "" || json.Valid(raw) == false {
		t.Fatalf("invalid config: %s", raw)
	}
	if containsText(string(raw), "top-secret") {
		t.Fatalf("credential leaked into project config: %s", raw)
	}
	if !containsText(string(raw), "{env:OPENAI_API_KEY}") {
		t.Fatalf("env reference missing: %s", raw)
	}
}

func TestPrepareExecutionDoesNotDuplicateOpenCodeV1Path(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	cwd := t.TempDir()
	cfg, err := c.ResolveExecutionConfig(context.Background(), ExecutionRequest{
		Surface: SurfaceTerminal, CLI: "opencode", Cwd: cwd,
		Local: &ChatLocalEndpoint{Endpoint: "https://llm.example/v1", Model: "qwen3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.PrepareExecution(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(cwd, "opencode.json"))
	if err != nil {
		t.Fatal(err)
	}
	if containsText(string(raw), "/v1/v1") {
		t.Fatalf("duplicated API path: %s", raw)
	}
}

func TestPreflightExecutionRoutesCompatibleAgenticRuntimeButRejectsTerminal(t *testing.T) {
	withTempConfigDir(t)
	c, _ := New(Options{Store: openTempStore(t)})
	agent := &Agent{ID: "managed-preflight", Name: "Managed", Instructions: "x", Supports: []string{"claude"}, Surfaces: append([]string(nil), AllSurfaces...)}
	if err := SaveAgentRuntime(agent.ID, &AgentRuntimeManifest{
		Schema: AgentRuntimeSchema, Mode: RuntimeAgentic,
		Features: AgentRuntimeFeatures{ManagedTools: true, WorkingMemory: true, Artifacts: true},
	}); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	workflow := c.PreflightExecution(context.Background(), ExecutionRequest{Surface: SurfaceWorkflow, Agent: agent, CLI: "claude", Cwd: cwd}, false)
	if !workflow.OK {
		t.Fatalf("managed workflow preflight = %#v", workflow)
	}
	foundInfo := false
	for _, issue := range workflow.Issues {
		foundInfo = foundInfo || issue.Code == "managed_policy_broker"
	}
	if !foundInfo {
		t.Fatalf("managed boundary not disclosed: %#v", workflow.Issues)
	}
	terminal := c.PreflightExecution(context.Background(), ExecutionRequest{Surface: SurfaceTerminal, Agent: agent, CLI: "claude", Cwd: cwd}, false)
	if terminal.OK {
		t.Fatalf("managed terminal preflight unexpectedly succeeded: %#v", terminal)
	}
}

func TestPreflightAcceptsManagedRAGWithKnowledgeBroker(t *testing.T) {
	withTempConfigDir(t)
	c, _ := New(Options{Store: openTempStore(t)})
	agent := &Agent{
		ID: "managed-rag-preflight", Name: "Managed RAG", Instructions: "x", Knowledge: "rag",
		Supports: []string{"claude"}, Surfaces: append([]string(nil), AllSurfaces...),
	}
	if err := SaveAgentRuntime(agent.ID, &AgentRuntimeManifest{
		Schema: AgentRuntimeSchema, Mode: RuntimeAgentic,
		Features: AgentRuntimeFeatures{ManagedTools: true, WorkingMemory: true, Artifacts: true},
	}); err != nil {
		t.Fatal(err)
	}
	knowledgeDir, err := AgentKnowledgeDir(agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(knowledgeDir, "graphify-out"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(knowledgeDir, "graphify-out", "graph.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	result := c.PreflightExecution(context.Background(), ExecutionRequest{
		Surface: SurfaceChat, Agent: agent, CLI: "claude", Cwd: t.TempDir(),
	}, false)
	if !result.OK {
		t.Fatalf("managed RAG preflight failed: %#v", result)
	}
}

func TestPreflightRejectsMissingWorkingFolder(t *testing.T) {
	c, _ := New(Options{Store: openTempStore(t)})
	result := c.PreflightExecution(context.Background(), ExecutionRequest{
		Surface: SurfaceChat, CLI: "claude", Cwd: filepath.Join(t.TempDir(), "missing"),
	}, false)
	if result.OK {
		t.Fatalf("preflight unexpectedly passed: %+v", result)
	}
}

func containsText(body, needle string) bool {
	for i := 0; i+len(needle) <= len(body); i++ {
		if body[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
