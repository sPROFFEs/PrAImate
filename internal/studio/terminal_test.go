package studio

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

func terminalExecutables(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "cli tools with spaces")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
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
	s, _ := fixture(t)
	dir := terminalExecutables(t)
	s.session.Model = "selected-model"
	t.Setenv("PRAIMATE_STUDIO_TOKEN", "test-secret-not-for-terminal")
	for _, cli := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
		before := s.sessionSnapshot()
		plan := rpcOK(t, s, "terminals.prepare", map[string]string{"cli": cli}).(*terminalPlan)
		if filepath.Dir(plan.Command) != dir || plan.Cwd != before.Workspace || plan.CLI != cli {
			t.Fatalf("wrong terminal plan: %+v", plan)
		}
		if cli == "claude" {
			if !reflect.DeepEqual(plan.Args, []string{"--model", "selected-model"}) {
				t.Fatal("selected model lost")
			}
		} else if len(plan.Args) != 0 {
			t.Fatal("model leaked into another CLI")
		}
		if len(plan.Env) != 1 || plan.Env["PATH"] != dir {
			t.Fatal("terminal exposed unexpected environment")
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
	if err != nil || len(chats) != 0 {
		t.Fatal("native terminal should not create a managed chat")
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
