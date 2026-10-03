package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt in with verified local files. No account, external endpoint, user state,
// or application modifications are used by this real-model protocol check.
func TestInstalledAssistantModelCompatibility(t *testing.T) {
	runtimePath := os.Getenv("PRAIMATE_TEST_LLAMA_RUNTIME")
	modelPath := os.Getenv("PRAIMATE_TEST_ASSISTANT_MODEL")
	if runtimePath == "" || modelPath == "" {
		t.Skip("set PRAIMATE_TEST_LLAMA_RUNTIME and PRAIMATE_TEST_ASSISTANT_MODEL")
	}
	if !filepath.IsAbs(runtimePath) || !filepath.IsAbs(modelPath) {
		t.Fatal("runtime and model fixture paths must be absolute")
	}
	c := DefaultConfig()
	c.Enabled, c.KeepLoaded, c.Temperature = true, true, 0
	if strings.Contains(strings.ToLower(filepath.Base(modelPath)), "qwen") {
		c.ModelID, c.Context = "qwen-quality", 4096
	}
	c.MaxTurns, c.MaxActions = 6, 4
	p := &LlamaProvider{Runtime: runtimePath, ModelPath: modelPath, Config: c}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := p.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	for _, tc := range []struct{ name, message, action string }{
		{"SpanishGreeting", "hola", ""},
		{"EnglishGreeting", "hello", ""},
		{"SpanishHelp", "¿Qué puedes hacer en PrAImate?", ""},
		{"Navigate", "Open settings.", "ui.navigate"},
		{"Discover", "List my chats.", "chats.list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRegistry()
			openedPage := ""
			r.Register(Action{Name: "actions.search", Description: "Discover available actions using English keywords", Capability: "read", Fields: map[string]Field{"query": {Type: "string", Required: true}}, Execute: func(_ context.Context, args map[string]any) (any, error) {
				return r.Search(args["query"].(string), 6), nil
			}})
			r.Register(Action{Name: "app.search", Description: "Search application entities by text", Capability: "read", Fields: map[string]Field{"query": {Type: "string", Required: true}}, Execute: func(context.Context, map[string]any) (any, error) { return []any{}, nil }})
			r.Register(Action{Name: "ui.navigate", Description: "Open dashboard, chats, workers, agents, skills, mcp, settings, code, studio or documents", Capability: "navigate", Fields: map[string]Field{"page": {Type: "string", Required: true}}, Execute: func(_ context.Context, args map[string]any) (any, error) {
				openedPage, _ = args["page"].(string)
				return map[string]any{"opened": args["page"]}, nil
			}})
			r.Register(Action{Name: "chats.list", Description: "List recent chats and their IDs", Capability: "read", Execute: func(context.Context, map[string]any) (any, error) { return []any{}, nil }})
			state := State{}
			s := New(Options{Registry: r, Config: func(context.Context) (Config, error) { return c, nil }, Provider: func(context.Context, Config) (Provider, error) { return p, nil }, Load: func(context.Context) (State, error) { return state, nil }, Save: func(_ context.Context, value State) error { state = value; return nil }})
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			result, err := s.Run(ctx, tc.message, map[string]any{"page": "dashboard"})
			if err != nil {
				t.Fatalf("%s: %v; steps=%+v", c.ModelID, err, result.Task.Steps)
			}
			if result.Task.Status != "completed" || len(result.Messages) != 2 || strings.TrimSpace(result.Messages[1].Text) == "" {
				t.Fatalf("no natural-language final response: %+v", result)
			}
			if tc.action == "" && len(result.Task.Steps) != 0 {
				t.Fatalf("conversation unexpectedly executed actions: %+v", result.Task.Steps)
			}
			if tc.action == "ui.navigate" && openedPage != "settings" {
				t.Fatalf("opened wrong page %q instead of settings", openedPage)
			}
			if tc.name == "SpanishHelp" {
				reply := strings.ToLower(result.Messages[1].Text)
				if !strings.Contains(reply, "chat") && !strings.Contains(reply, "agent") && !strings.Contains(reply, "worker") && !strings.Contains(reply, "studio") {
					t.Fatal("capability answer did not describe any actual application feature")
				}
			}
			if tc.action != "" {
				found := false
				for _, step := range result.Task.Steps {
					found = found || step.Action == tc.action && step.Status == "completed"
				}
				if !found {
					t.Fatalf("requested action was not executed: %+v", result.Task.Steps)
				}
			}
			t.Logf("%s: %s; steps=%d", c.ModelID, result.Messages[1].Text, len(result.Task.Steps))
		})
	}
}
