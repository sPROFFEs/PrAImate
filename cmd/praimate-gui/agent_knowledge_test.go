package main

import (
	"context"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/knowledge"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestRAGContextHasNoDeadlineAndCancelsWithApp(t *testing.T) {
	appCtx, stopApp := context.WithCancel(context.Background())
	defer stopApp()

	ragCtx, stopRAG := newRAGContext(appCtx)
	defer stopRAG()
	if _, ok := ragCtx.Deadline(); ok {
		t.Fatal("RAG context must not impose a total execution deadline")
	}

	stopApp()
	select {
	case <-ragCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("RAG context was not cancelled when the application stopped")
	}
}

func TestAgentKnowledgeRemoteBindingsPreservePrivateKey(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	a := assistantAppFixture(t)
	a.ctx = context.Background()
	_, err := a.core.ImportAgentYAML(a.ctx, []byte("schema: praimate.agent/v1\nid: remote-binding\nname: Remote\ninstructions: Use references.\nsupports: [codex]\nknowledge: rag\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(knowledge.HTTPHandler(t.TempDir(), "test-key"))
	defer server.Close()
	if err = a.SaveAgentKnowledgeConfig("remote-binding", `{"source":"remote","endpoint":"`+server.URL+`"}`, "test-key", false); err != nil {
		t.Fatal(err)
	}
	info, err := a.GetAgentKnowledge("remote-binding")
	if err != nil || info.RetrievalEngine != "remote" || !info.HasAPIKey || info.HasIndex {
		t.Fatalf("remote GUI state: %+v %v", info, err)
	}
	if err = a.TestAgentKnowledgeRemote("remote-binding"); err != nil {
		t.Fatal(err)
	}
	if _, err = a.core.BuildAgentKnowledge(a.ctx, "remote-binding"); err == nil {
		t.Fatal("remote corpus indexed locally")
	}
}

func TestOpenAIBaseURLRepairsMissingSchemeSlashes(t *testing.T) {
	tests := map[string]string{
		"http:192.168.1.50:11434":              "http://192.168.1.50:11434/v1",
		"https:llm.example":                    "https://llm.example/v1",
		"192.168.1.50:11434":                   "http://192.168.1.50:11434/v1",
		"https://llm.example/openai/v1":        "https://llm.example/openai/v1",
		"https://llm.example/openai/v1/":       "https://llm.example/openai/v1",
		"  https://llm.example/openai/v1///  ": "https://llm.example/openai/v1",
	}
	for input, want := range tests {
		if got := openAIBaseURL(input); got != want {
			t.Errorf("openAIBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAgentRAGCanBeCancelled(t *testing.T) {
	a := NewApp()
	a.ctx = context.Background()

	ragCtx, done, err := a.beginRAG("agent-1")
	if err != nil {
		t.Fatalf("beginRAG: %v", err)
	}
	defer done()

	a.CancelAgentRAG("agent-1")
	select {
	case <-ragCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("CancelAgentRAG did not cancel the active extraction")
	}
}

func TestRequirementsCommandUsesBashForUnixShellScripts(t *testing.T) {
	name, args := requirementsCommand("linux", "/tmp/setup.sh")

	if name != "bash" {
		t.Fatalf("command = %q, want bash", name)
	}
	if want := []string{"/tmp/setup.sh"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestPrepareSudoAskpassUsesConfiguredHelper(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "askpass")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SUDO_ASKPASS", helper)

	got, cleanup, err := prepareSudoAskpass("linux")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if got != helper {
		t.Fatalf("helper = %q, want %q", got, helper)
	}
}

func TestPrepareSudoPopupWrapperUsesPolicyKit(t *testing.T) {
	pkexec := filepath.Join(t.TempDir(), "pkexec")
	if err := os.WriteFile(pkexec, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	dir, cleanup, err := prepareSudoPopupWrapper(pkexec, "/usr/bin/sudo")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := exec.Command(filepath.Join(dir, "sudo"), "apt-get", "update").CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapper: %v: %s", err, out)
	}
	if got, want := string(out), "/usr/bin/sudo\napt-get\nupdate\n"; got != want {
		t.Fatalf("wrapper args = %q, want %q", got, want)
	}
}

func TestAgentRequirementsCanBeCancelled(t *testing.T) {
	a := NewApp()
	a.ctx = context.Background()

	ctx, done, err := a.beginRequirements("agent-1")
	if err != nil {
		t.Fatalf("beginRequirements: %v", err)
	}
	defer done()
	if _, _, err := a.beginRequirements("agent-1"); err == nil {
		t.Fatal("second requirements run for the same agent must be rejected")
	}

	a.CancelAgentRequirements("agent-1")
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("CancelAgentRequirements did not cancel the active script")
	}
}

func TestAgentRAGBuiltInUsesCoreAndReportsIndex(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	a := assistantAppFixture(t)
	a.ctx = context.Background()
	a.ragCancels = map[string]*ragRun{}
	raw := []byte("schema: praimate.agent/v1\nid: offline-rag\nname: Offline RAG\ninstructions: Review source material.\nsupports: [praimate-cli]\nknowledge: rag\n")
	if _, err := a.core.ImportAgentYAML(a.ctx, raw, ""); err != nil {
		t.Fatal(err)
	}
	dir, err := core.AgentKnowledgeDir("offline-rag")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# JWT\nRefresh every 900 seconds."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = a.BuildAgentRAG("offline-rag", "native", "", ""); err != nil {
		t.Fatal(err)
	}
	info, err := a.GetAgentKnowledge("offline-rag")
	if err != nil || !info.NativeIndex || !info.HasIndex || len(info.Files) != 1 {
		t.Fatalf("knowledge info: %#v %v", info, err)
	}
	if err = os.Remove(filepath.Join(dir, ".praimate-index", "index.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(dir, "graphify-out"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err = a.GetAgentKnowledge("offline-rag")
	if err != nil || info.HasIndex || info.RetrievalEngine != "native" {
		t.Fatalf("empty graph directory marked ready: %#v %v", info, err)
	}
}
