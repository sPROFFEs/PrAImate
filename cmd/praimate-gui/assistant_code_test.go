package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func prepareAssistantTerminalFixture(t *testing.T, app *App) {
	t.Helper()
	core.RegisterAllCLIAdapters()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := filepath.Join(os.Getenv("PRAIMATE_HOME"), "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Spawn a real PTY without accounts, model downloads or project changes.
	if err := os.WriteFile(filepath.Join(bin, "praimate-cli"), []byte("#!/bin/sh\nprintf 'assistant-terminal-ready\\n'\nread line\n"), 0700); err != nil {
		t.Fatal(err)
	}
	app.terms = newTermManager()
	t.Cleanup(app.terms.closeAll)
}

func verifyAssistantTerminal(t *testing.T, app *App, navigation <-chan map[string]any) {
	t.Helper()
	sessions := app.terms.list()
	if len(sessions) != 1 || sessions[0].ChatID == "" {
		t.Fatalf("no bound terminal: %+v", sessions)
	}
	chat, err := app.core.GetChat(context.Background(), sessions[0].ChatID)
	if err != nil || chat.Settings.Surface != "code" || chat.CLIAgent != "praimate-cli" || chat.WorkspacePath != sessions[0].Cwd {
		t.Fatalf("terminal does not match the saved Code session: chat=%+v err=%v", chat, err)
	}
	select {
	case event := <-navigation:
		if event["page"] != "code" || event["term_id"] != sessions[0].ID || event["chat_id"] != sessions[0].ChatID {
			t.Fatalf("UI received wrong terminal attachment: %+v", event)
		}
	default:
		t.Fatal("terminal created without a UI attachment event")
	}
}

func TestAssistantCodeStartCreatesBoundTerminal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY fixture uses a POSIX shell")
	}
	app := assistantAppFixture(t)
	app.ctx = context.Background()
	navigation := make(chan map[string]any, 1)
	app.emit = func(_ context.Context, event string, values ...any) {
		if event == "assistant:navigate" {
			navigation <- values[0].(map[string]any)
		}
	}
	prepareAssistantTerminalFixture(t, app)
	action, ok := app.assistantActions().Get("code.start")
	if !ok {
		t.Fatal("missing terminal action")
	}
	args := map[string]any{"cli": "praimate-cli", "workspace": t.TempDir()}
	if err := action.Validate(args); err != nil {
		t.Fatal(err)
	}
	if _, err := action.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	verifyAssistantTerminal(t, app, navigation)
}

func TestAssistantCodeStartDoesNotCreateSessionForInvalidRequest(t *testing.T) {
	app := assistantAppFixture(t)
	action, _ := app.assistantActions().Get("code.start")
	if err := action.Validate(map[string]any{"cli": "praimate-cli"}); err == nil {
		t.Fatal("accepted a missing workspace")
	}
	for _, path := range []string{"relative-project", filepath.Join(t.TempDir(), "missing")} {
		if _, err := action.Execute(context.Background(), map[string]any{"cli": "praimate-cli", "workspace": path}); err == nil {
			t.Fatal("accepted invalid workspace")
		}
	}
	chats, err := app.core.ListChats(context.Background(), 20)
	if err != nil || len(chats) != 0 {
		t.Fatalf("invalid launch left a saved chat: %+v err=%v", chats, err)
	}
}

func TestAssistantAppDiscoveryFindsRequestedOperations(t *testing.T) {
	r := (&App{}).assistantActions()
	for _, tc := range []struct{ query, want string }{
		{"Crea un agente de test", "agents.create"},
		{"Abre un terminal de código con praimate-cli", "code.start"},
		{"Abre la página Code", "ui.navigate"},
		{"Cambia el modelo del worker rápido", "workers.set_model"},
		{"Pon el tema oscuro", "settings.appearance"},
	} {
		found := r.Search(tc.query, 6)
		if len(found) == 0 || found[0].Name != tc.want {
			t.Errorf("%q: wanted %s first, got %+v", tc.query, tc.want, found)
		}
	}
}
