package studio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

func terminalExecutables(t *testing.T) string {
	t.Helper()
	// Isolate managed CLI discovery from executables installed by the developer.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("LOCALAPPDATA", home)
	dir := filepath.Join(t.TempDir(), "cli tools with spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code", "praimate-cli", "copilot", "agy"} {
		if runtime.GOOS == "windows" {
			cli += ".exe"
		}
		// Resolution must not run probes or this non-executable fixture.
		if err := os.WriteFile(filepath.Join(dir, cli), []byte("fixture only"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	return dir
}

func TestTerminalPlanUsesSelectedCLIAndModelWithoutMutatingSession(t *testing.T) {
	dir := terminalExecutables(t)
	s, _ := fixture(t)
	s.session.Model = "selected-model"
	t.Setenv("PRAIMATE_STUDIO_TOKEN", "test-secret-not-for-terminal")
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code", "praimate-cli", "copilot", "antigravity"} {
		before := s.sessionSnapshot()
		plan := rpcOK(t, s, "terminals.prepare", map[string]string{"cli": cli}).(*terminalPlan)
		if filepath.Dir(plan.Command) != dir || plan.Cwd != before.Workspace || plan.CLI != cli {
			t.Fatalf("wrong terminal plan: %+v", plan)
		}
		if cli == "claude" {
			if !reflect.DeepEqual(plan.Args, []string{"--model", "selected-model"}) {
				t.Fatal("selected model lost")
			}
		} else if cli == "praimate-cli" {
			if len(plan.Args) != 2 || plan.Args[0] != "--chat" {
				t.Fatalf("native terminal lost core session: %v", plan.Args)
			}
			chat, err := s.core.GetChat(context.Background(), plan.Args[1])
			if err != nil || chat.CLIAgent != cli || chat.WorkspacePath != before.Workspace {
				t.Fatalf("native chat: %+v %v", chat, err)
			}
		} else if cli != "codex" && len(plan.Args) != 0 {
			t.Fatal("model leaked into another CLI")
		}
		if plan.Env["PATH"] != dir || plan.Env["PRAIMATE_STUDIO_TOKEN"] != "" {
			t.Fatal("terminal exposed unexpected environment")
		}
		if cli == "codex" && plan.Env["OTEL_EXPORTER_OTLP_LOGS_HEADERS"] != "Authorization=Bearer "+plan.Env["PRAIMATE_USAGE_TOKEN"] {
			t.Fatal("Codex terminal lost its authenticated usage exporter")
		}
		if cli != "praimate-cli" && cli != "antigravity" && plan.UsageID == "" {
			t.Fatal("missing terminal usage receiver")
		}
		if plan.UsageID != "" {
			if len(s.terminalUsage) != 1 {
				t.Fatal("receiver leaked between launches")
			}
			rpcOK(t, s, "terminals.closed", map[string]string{"usageId": plan.UsageID})
			if len(s.terminalUsage) != 0 {
				t.Fatal("terminal receiver not closed")
			}
		}
		if !reflect.DeepEqual(before, s.sessionSnapshot()) {
			t.Fatal("opening a terminal changed the chat configuration")
		}
	}
	for _, cli := range rpcOK(t, s, "terminals.list", nil).([]terminalCLI) {
		if !cli.Available {
			t.Fatalf("executable %s was not found", cli.ID)
		}
	}
	chats, err := s.core.ListChats(context.Background(), 0)
	if err != nil || len(chats) != 1 || chats[0].CLIAgent != "praimate-cli" {
		t.Fatal("only the core-native terminal should create a shared core chat")
	}
}

func TestTerminalPlanRejectsMissingUnknownAndUnauthenticatedLaunches(t *testing.T) {
	s, _ := fixture(t)
	t.Setenv("PATH", t.TempDir())
	for _, cli := range []string{"claude", "unknown", "/bin/sh"} {
		if _, err := s.prepareTerminal([]byte(`{"cli":"` + cli + `"}`)); err == nil {
			t.Fatalf("accepted %s", cli)
		}
	}
	s.session.Workspace = ""
	if _, err := s.prepareTerminal([]byte(`{}`)); err == nil {
		t.Fatal("accepted workspace-less launch")
	}
	s.token = "test-only"
	if response := s.dispatch(RPCRequest{JSONRPC: "2.0", ID: 1, Method: "terminals.prepare"}); response.Error == nil {
		t.Fatal("unauthenticated launch plan")
	}
}

func TestNativeTerminalSnapshotPreservesCoreConfiguration(t *testing.T) {
	terminalExecutables(t)
	s, _ := fixture(t)
	s.session.CLI = "praimate-cli"
	s.session.AgentID = "dev-team"
	s.session.Tools = "edits"
	s.session.Model = "model-pin"
	s.session.LocalEndpoint = "http://127.0.0.1:11434/v1"
	s.session.LocalModel = "local-model"
	s.session.MCPServers = []string{}
	plan, err := s.prepareTerminal([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	chat, err := s.core.GetChat(context.Background(), plan.Args[1])
	if err != nil {
		t.Fatal(err)
	}
	if chat.AgentID != "dev-team" || chat.Settings.Tools != "edits" || chat.Settings.Model != "model-pin" || !chat.Settings.MCPConfigured || chat.Settings.Local == nil || chat.Settings.Local.Model != "local-model" {
		t.Fatalf("terminal snapshot lost core configuration: %+v", chat)
	}
	if len(plan.Env) != 1 || len(plan.Args) != 2 {
		t.Fatalf("native terminal exposed configuration in launch plan: %+v", plan)
	}
}

func TestTerminalResolvesManagedPraimateCodeOutsidePATH(t *testing.T) {
	s, _ := fixture(t)
	t.Setenv("PATH", t.TempDir())
	dir := filepath.Join(os.Getenv("PRAIMATE_HOME"), "bin")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	name := "praimate-code"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	want := filepath.Join(dir, name)
	if err := os.WriteFile(want, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	resolved, err := core.ResolveInteractiveCLIBinary("praimate-code")
	if err != nil || resolved != want {
		t.Fatalf("managed binary: %q %v", resolved, err)
	}
	_ = s
}
