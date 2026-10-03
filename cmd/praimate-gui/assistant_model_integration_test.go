package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/orchestrator"
)

type recordedAssistantModel struct {
	assistant.Provider
	t *testing.T
}

func (p recordedAssistantModel) Generate(ctx context.Context, r assistant.Request) (string, error) {
	raw, err := p.Provider.Generate(ctx, r)
	p.t.Logf("decision: %s; error: %v", raw, err)
	return raw, err
}

// Opt in with verified local artifacts. Use the complete application registry
// and isolated encrypted state, so a fluent reply alone cannot pass this test.
func TestInstalledAssistantAppConfiguration(t *testing.T) {
	runtimePath, modelPath := os.Getenv("PRAIMATE_TEST_LLAMA_RUNTIME"), os.Getenv("PRAIMATE_TEST_ASSISTANT_MODEL")
	if runtimePath == "" || modelPath == "" {
		t.Skip("set PRAIMATE_TEST_LLAMA_RUNTIME and PRAIMATE_TEST_ASSISTANT_MODEL")
	}
	if !filepath.IsAbs(runtimePath) || !filepath.IsAbs(modelPath) {
		t.Fatal("runtime and model fixture paths must be absolute")
	}
	c := assistant.DefaultConfig()
	c.Enabled, c.KeepLoaded, c.Temperature = true, true, 0
	c.Permissions = assistant.Preset("full")
	c.MaxTurns, c.MaxActions = 5, 4
	if strings.Contains(strings.ToLower(filepath.Base(modelPath)), "qwen") {
		c.ModelID, c.Context = "qwen-quality", 4096
	}
	p := &assistant.LlamaProvider{Runtime: runtimePath, ModelPath: modelPath, Config: c}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct{ name, message string }{
		{"SpanishAgent", "Crea un agente de test llamado test."},
		{"SpanishWorker", "Cambia el modelo del worker rápido a qwen-test."},
		{"EnglishWorker", "Set the fast worker's model to qwen-test."},
		{"SpanishCode", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := assistantAppFixture(t)
			app.ctx = context.Background()
			navigation := make(chan map[string]any, 4)
			app.emit = func(_ context.Context, event string, values ...any) {
				if event == "assistant:navigate" {
					navigation <- values[0].(map[string]any)
				}
			}
			config := orchestrator.Config{Workspace: t.TempDir()}
			message := tc.message
			if tc.name == "SpanishCode" {
				if runtime.GOOS == "windows" {
					t.Skip("PTY fixture uses a POSIX shell")
				}
				prepareAssistantTerminalFixture(t, app)
				message = fmt.Sprintf("Abre un terminal de código con praimate-cli en %s.", config.Workspace)
			}
			for _, tier := range []orchestrator.Tier{orchestrator.Primary, orchestrator.Middle, orchestrator.Fast} {
				config.Profiles = append(config.Profiles, orchestrator.Profile{Tier: tier, Runtime: "cli", CLI: "codex", Model: "previous-model", TimeoutSeconds: 60, MaxInputBytes: 65536})
			}
			raw, _ := json.Marshal(config)
			if err := app.SaveWorkerConfig(string(raw)); err != nil {
				t.Fatal(err)
			}
			state := assistant.State{}
			s := assistant.New(assistant.Options{Registry: app.assistantActions(), Config: func(context.Context) (assistant.Config, error) { return c, nil }, Provider: func(context.Context, assistant.Config) (assistant.Provider, error) {
				return recordedAssistantModel{Provider: p, t: t}, nil
			}, Load: func(context.Context) (assistant.State, error) { return state, nil }, Save: func(_ context.Context, value assistant.State) error { state = value; return nil }})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			result, err := s.Run(ctx, message, map[string]any{"page": "workers"})
			if err != nil {
				t.Fatalf("%s: %v; task=%+v", c.ModelID, err, result.Task)
			}
			if tc.name == "SpanishAgent" {
				agents, err := app.ListAgents()
				if err != nil || len(agents) != 1 || agents[0].Name != "test" || agents[0].Instructions == "" || len(agents[0].Tools) != 0 || len(agents[0].MCPServers) != 0 || result.Task.Status != "completed" {
					t.Fatalf("test agent was not created without implicit grants: agents=%+v; error=%v; task=%+v", agents, err, result.Task)
				}
				t.Logf("%s: saved agent %s (%s)", c.ModelID, agents[0].Name, agents[0].ID)
				return
			}
			if tc.name == "SpanishCode" {
				verifyAssistantTerminal(t, app, navigation)
				if sessions := app.terms.list(); len(sessions) != 1 || sessions[0].Cwd != config.Workspace {
					t.Fatalf("terminal opened in a different folder than requested: %+v; want %s", sessions, config.Workspace)
				}
				if result.Task.Status != "completed" {
					t.Fatalf("terminal task did not complete: %+v", result.Task)
				}
				return
			}
			updated, err := app.WorkerConfig()
			if err != nil {
				t.Fatal(err)
			}
			for _, profile := range updated.Profiles {
				want := "previous-model"
				if profile.Tier == orchestrator.Fast {
					want = "qwen-test"
				}
				if profile.Model != want || profile.CLI != "codex" || profile.AllowCommands || profile.AllowEdits {
					t.Fatalf("requested configuration was not applied correctly: profile=%+v; task=%+v; messages=%+v", profile, result.Task, result.Messages)
				}
			}
			if result.Task.Status != "completed" {
				t.Fatalf("operation did not complete: %+v", result.Task)
			}
			t.Logf("%s: %s; steps=%+v", c.ModelID, result.Messages[len(result.Messages)-1].Text, result.Task.Steps)
		})
	}
}
