package core

import (
	"context"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

func TestLocalModelExecutionKeepsEachProviderCredential(t *testing.T) {
	c := nativeTestCore(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	for _, host := range []LocalHost{{ID: "a", Endpoint: "https://a.test", APIKey: "key-a"}, {ID: "b", Endpoint: "https://b.test", APIKey: "key-b"}} {
		if _, err := c.SaveLocalHost(ctx, host); err != nil {
			t.Fatal(err)
		}
		if _, err := ollama.ApplyOpenCodeModels("praimate_"+host.ID, host.ID, ollama.Settings{Endpoint: host.Endpoint, APIKey: host.APIKey}, []string{"same/model"}, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, cli := range []string{"opencode", "praimate-code", "openclaude"} {
		cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: SurfaceChat, CLI: cli, Cwd: t.TempDir(), Local: &ChatLocalEndpoint{Endpoint: "https://b.test", Model: "same/model"}})
		if err != nil || cfg.Env["OPENAI_API_KEY"] != "key-b" {
			t.Fatalf("%s lost non-default host credential: %v", cli, err)
		}
	}
	for _, model := range []string{"praimate_a/same/model", "praimate_b/same/model"} {
		cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: SurfaceTerminal, CLI: "praimate-code", Cwd: t.TempDir(), Model: model})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Env[ollama.OpenCodeAPIKeyEnv("praimate_a")] != "key-a" || cfg.Env[ollama.OpenCodeAPIKeyEnv("praimate_b")] != "key-b" {
			t.Fatal("provider keys were mixed")
		}
	}
	key, err := c.localEndpointCredential(ctx, "https://unregistered.test")
	if err != nil || key != "" {
		t.Fatal("default credential leaked to unrelated endpoint")
	}
	if _, err := c.SaveLocalHost(ctx, LocalHost{ID: "b", Endpoint: "https://b.test", RemoveAPIKey: true}); err != nil {
		t.Fatal(err)
	}
	cfg, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: SurfaceTerminal, CLI: "praimate-code", Cwd: t.TempDir(), Model: "praimate_b/same/model"})
	if err != nil {
		t.Fatal(err)
	}
	if value, exists := cfg.Env["OPENAI_API_KEY"]; !exists || value != "" || cfg.Env[ollama.OpenCodeAPIKeyEnv("praimate_b")] != "" {
		t.Fatal("removed key can fall back to an inherited credential")
	}
}
