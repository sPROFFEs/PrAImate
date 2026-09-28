package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestWorkflowResolverInjectsEncryptedKeyForManagedOpenCode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://llm.example"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	if err := saveLocalLLMAPIKey(c, "db-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := ollama.ApplyOpenCode(ollama.Settings{
		Endpoint: "https://llm.example", Model: "qwen3", APIKey: "db-secret",
	}, true); err != nil {
		t.Fatal(err)
	}

	effective, err := c.ResolveExecutionConfig(context.Background(), core.ExecutionRequest{
		Surface: core.SurfaceWorkflow, CLI: "praimate-code", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if effective.Model != "" {
		t.Fatalf("model = %q, want CLI configured default", effective.Model)
	}
	if got := effective.Env["OPENAI_API_KEY"]; got != "db-secret" {
		t.Fatalf("OPENAI_API_KEY = %q, want encrypted DB credential", got)
	}
}

func TestTerminalResolverInjectsEncryptedKeyForManagedOpenCode(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://llm.example"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	if err := saveLocalLLMAPIKey(c, "terminal-secret"); err != nil {
		t.Fatal(err)
	}
	if _, err := ollama.ApplyOpenCode(ollama.Settings{
		Endpoint: "https://llm.example", Model: "qwen3", APIKey: "terminal-secret",
	}, true); err != nil {
		t.Fatal(err)
	}

	effective, err := c.ResolveExecutionConfig(context.Background(), core.ExecutionRequest{
		Surface: core.SurfaceTerminal, CLI: "opencode", Cwd: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := effective.Env["OPENAI_API_KEY"]; got != "terminal-secret" {
		t.Fatalf("managed OpenCode terminal key = %q", got)
	}
}

func TestApplyLocalToCLIRejectsCodex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://llm.example"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	a := &App{core: c}
	if _, err := a.ApplyLocalToCLI("codex", "qwen3"); err == nil {
		t.Fatal("Codex local routing must not be accepted")
	}
}
