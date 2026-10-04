package core

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeBinOnPath writes an executable shell script named `name` into a
// temp dir and prepends it to PATH for the test. Unix-only — callers
// must skip on Windows.
func fakeBinOnPath(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("sh-script fake binaries don't run on Windows")
	}
}

func TestExecAdapter_StdoutReply(t *testing.T) {
	skipOnWindows(t)
	// opencode-style: message arrives on stdin now, echo it back.
	fakeBinOnPath(t, "opencode", `echo "reply: $(cat -)"`)
	a := NewOpenCodeAdapter()
	r, err := a.SingleShot(context.Background(), SingleShotOpts{Cwd: t.TempDir(), Message: "hello"})
	if err != nil {
		t.Fatalf("SingleShot: %v", err)
	}
	if !strings.Contains(r.Text, "hello") {
		t.Fatalf("reply did not include message: %q", r.Text)
	}
}

func TestOpenCodeAdapter_StreamErrorFailsTurn(t *testing.T) {
	skipOnWindows(t)
	fakeBinOnPath(t, "opencode", `
printf '%s\n' '{"type":"text","part":{"text":"partial reply"}}'
printf '%s\n' '{"type":"error","error":{"message":"tool failed"}}'
`)
	a := NewOpenCodeAdapter()
	r, err := a.SingleShot(context.Background(), SingleShotOpts{Cwd: t.TempDir(), Message: "hello"})
	if err == nil {
		t.Fatal("expected OpenCode stream error to fail the turn")
	}
	if !strings.Contains(err.Error(), "tool failed") {
		t.Fatalf("error did not include OpenCode message: %v", err)
	}
	if r == nil || r.Text != "partial reply" {
		t.Fatalf("partial reply should be preserved for callers that can use it, got %+v", r)
	}
}

func TestExecAdapter_SystemPromptPrepended(t *testing.T) {
	skipOnWindows(t)
	fakeBinOnPath(t, "opencode", `cat -`)
	a := NewOpenCodeAdapter()
	r, _ := a.SingleShot(context.Background(), SingleShotOpts{
		Cwd: t.TempDir(), Message: "do it", SystemPrompt: "BE-TERSE",
	})
	if !strings.Contains(r.Text, "BE-TERSE") {
		t.Fatalf("system prompt not prepended: %q", r.Text)
	}
}

// Pins the truncation fix: a multi-line system prompt + message must
// reach the CLI intact. Before the stdin switch, the message rode argv,
// which Windows .CMD shims truncate at the first newline (the "your
// prompt cuts off after ..." bug).
func TestExecAdapter_MultilineMessageSurvives(t *testing.T) {
	skipOnWindows(t)
	fakeBinOnPath(t, "opencode", `cat -`)
	a := NewOpenCodeAdapter()
	system := "You are the Agent Builder. Your job is to help the user\ncreate a new agent and hand them a valid YAML file."
	r, err := a.SingleShot(context.Background(), SingleShotOpts{
		Cwd: t.TempDir(), Message: "line1\nline2\nline3", SystemPrompt: system,
	})
	if err != nil {
		t.Fatalf("SingleShot: %v", err)
	}
	for _, want := range []string{"valid YAML file", "line3"} {
		if !strings.Contains(r.Text, want) {
			t.Fatalf("multi-line content truncated, missing %q: %q", want, r.Text)
		}
	}
}

func TestCodexAdapter_ReadsReplyFile(t *testing.T) {
	skipOnWindows(t)
	// codex writes the final message to the file after --output-last-message.
	fakeBinOnPath(t, "codex", `
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "--output-last-message" ]; then out="$2"; shift 2; continue; fi
  shift
done
printf 'FINAL-MSG' > "$out"
echo "noise on stdout"`)
	a := NewCodexAdapter()
	r, err := a.SingleShot(context.Background(), SingleShotOpts{Cwd: t.TempDir(), Message: "x"})
	if err != nil {
		t.Fatalf("SingleShot: %v", err)
	}
	if r.Text != "FINAL-MSG" {
		t.Fatalf("expected reply from file, got %q", r.Text)
	}
}

// Pure unit tests for the argv shapes — these run on every OS and pin
// that no adapter embeds the message in argv when stdinMsg is set.
func TestExecAdapter_StdinAdaptersKeepMessageOutOfArgv(t *testing.T) {
	const msg = "multi\nline\nmessage"
	cases := []*execAdapter{
		NewCodexAdapter(),
		NewOpenCodeAdapter(),
		NewPraimateCodeAdapter(),
	}
	for _, a := range cases {
		if !a.stdinMsg {
			t.Errorf("%s: expected stdinMsg=true", a.name)
		}
		args, _ := a.build(buildIn{Message: msg, TmpDir: t.TempDir()})
		for _, arg := range args {
			if strings.Contains(arg, "multi") {
				t.Errorf("%s: message leaked into argv: %q", a.name, args)
			}
		}
	}
}

func TestCodexAdapter_UsesStdinSentinel(t *testing.T) {
	args, _ := NewCodexAdapter().build(buildIn{Message: "ignored", TmpDir: t.TempDir()})
	if args[len(args)-1] != "-" {
		t.Fatalf("codex argv must end with '-' (read prompt from stdin), got %v", args)
	}
}

func TestCodexStreamSubmitsTaskAndPreservesActivityAtDeadline(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()
	t.Setenv("PRAIMATE_CODEX_FIXTURE_INPUT", filepath.Join(root, "input"))
	t.Setenv("PRAIMATE_CODEX_FIXTURE_ARGS", filepath.Join(root, "args"))
	fakeBinOnPath(t, "codex", `
printf '%s\n' "$@" > "$PRAIMATE_CODEX_FIXTURE_ARGS"
cat > "$PRAIMATE_CODEX_FIXTURE_INPUT"
printf '%s\n' '{"type":"thread.started","thread_id":"started-worker"}' '{"type":"turn.started"}' '{"type":"item.completed","item":{"id":"r","type":"reasoning","text":"Planning task dependencies."}}' '{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"partial plan"}}'
exec sleep 30
`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	emit, events := collectEvents()
	reply, err := NewCodexAdapter().SingleShotStream(ctx, SingleShotOpts{Cwd: root, Message: "Create the plan", SystemPrompt: "Return task JSON", Model: "gpt-6.1-sol", ReasoningEffort: "medium"}, emit)
	if !errors.Is(err, context.DeadlineExceeded) || reply == nil || reply.Text != "partial plan" || reply.SessionID != "started-worker" {
		t.Fatalf("deadline lost partial activity: %+v %v", reply, err)
	}
	prompt, readErr := os.ReadFile(filepath.Join(root, "input"))
	if readErr != nil || string(prompt) != "Return task JSON\n\nCreate the plan" {
		t.Fatalf("task was not sent over stdin: %q %v", prompt, readErr)
	}
	args, readErr := os.ReadFile(filepath.Join(root, "args"))
	if readErr != nil || !strings.Contains(string(args), "--json\n") || !strings.Contains(string(args), "-m\ngpt-6.1-sol\n") || !strings.Contains(string(args), "model_reasoning_effort=\"medium\"\n") || !strings.Contains(string(args), "--sandbox\nread-only\n") || !strings.HasSuffix(string(args), "-\n") {
		t.Fatalf("wrong Codex invocation: %q %v", args, readErr)
	}
	var started, reasoning bool
	for _, event := range *events {
		started = started || event.Type == "status" && strings.Contains(event.Detail, "process started")
		reasoning = reasoning || event.Type == "reasoning"
	}
	if !started || !reasoning {
		t.Fatalf("missing activity before timeout: %+v", *events)
	}
}

func TestCodexStreamFailedTurnCannotReturnSuccessEvenWithPartialOutput(t *testing.T) {
	skipOnWindows(t)
	fakeBinOnPath(t, "codex", `
cat > /dev/null
printf '%s\n' '{"type":"item.completed","item":{"id":"m","type":"agent_message","text":"partial answer"}}' '{"type":"turn.failed","error":{"message":"Model is unavailable"}}'
`)
	reply, err := NewCodexAdapter().SingleShotStream(context.Background(), SingleShotOpts{Cwd: t.TempDir(), Message: "Plan"}, nil)
	if err == nil || !strings.Contains(err.Error(), "Model is unavailable") || reply == nil || reply.Text != "partial answer" {
		t.Fatalf("failed turn was reported as successful or lost output: %+v %v", reply, err)
	}
}

// Pins the per-CLI model flag shapes: each adapter that supports a
// model override must put it in argv with its CLI's flag, and codex's
// stdin sentinel must stay LAST.
func TestExecAdapter_ModelFlagShapes(t *testing.T) {
	cases := []struct {
		adapter *execAdapter
		want    []string
	}{
		{NewCodexAdapter(), []string{"-m", "gpt-5.1-codex", "-"}},
		{NewOpenCodeAdapter(), []string{"run", "--model", "anthropic/claude-sonnet-4-5"}},
		{NewPraimateCodeAdapter(), []string{"run", "--model", "anthropic/claude-sonnet-4-5"}},
	}
	for _, tc := range cases {
		model := tc.want[len(tc.want)-1]
		if model == "-" {
			model = tc.want[len(tc.want)-2]
		}
		args, _ := tc.adapter.build(buildIn{Message: "msg", Model: model, TmpDir: t.TempDir()})
		got := strings.Join(args, " ")
		if !strings.Contains(got, strings.Join(tc.want, " ")) {
			t.Errorf("%s: argv %q does not contain %q", tc.adapter.name, got, strings.Join(tc.want, " "))
		}
	}
}

// Pins the per-CLI tools (permission level) flag shapes, and that the
// codex stdin sentinel still stays LAST with tools flags present.
func TestExecAdapter_ToolsFlagShapes(t *testing.T) {
	edits, _ := NewCodexAdapter().build(buildIn{Message: "msg", Tools: "edits", TmpDir: t.TempDir()})
	if got := strings.Join(edits, " "); !strings.Contains(got, "--sandbox workspace-write") {
		t.Errorf("codex edits: argv %q lacks --sandbox workspace-write", got)
	}
	full, _ := NewCodexAdapter().build(buildIn{Message: "msg", Tools: "full", TmpDir: t.TempDir()})
	if got := strings.Join(full, " "); !strings.Contains(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("codex full: argv %q lacks bypass flag", got)
	}
	if full[len(full)-1] != "-" {
		t.Errorf("codex full: stdin sentinel must stay last, got %v", full)
	}
	// Safe must not inherit Codex's own default, which can be writable.
	safe, _ := NewCodexAdapter().build(buildIn{Message: "msg", TmpDir: t.TempDir()})
	if got := strings.Join(safe, " "); !strings.Contains(got, "--sandbox read-only") {
		t.Errorf("codex safe: argv %q lacks --sandbox read-only", got)
	}
	// CLIs without permission flags in their legacy build functions must
	// not leak the level into argv. OpenCode permission flags are applied
	// in runOpenCodeJSON, not in build().
	for _, a := range []*execAdapter{NewOpenCodeAdapter(), NewPraimateCodeAdapter()} {
		args, _ := a.build(buildIn{Message: "msg", Tools: "full", TmpDir: t.TempDir()})
		for _, arg := range args {
			if arg == "full" || strings.Contains(arg, "dangerously") {
				t.Errorf("%s: tools level leaked into argv: %v", a.name, args)
			}
		}
	}
}

func TestOpenCodeModeArgs(t *testing.T) {
	if got := strings.Join(openCodeModeArgs("plan"), " "); got != "--agent plan" {
		t.Fatalf("plan tools args = %q", got)
	}
	if got := strings.Join(openCodeModeArgs("full"), " "); got != "--dangerously-skip-permissions" {
		t.Fatalf("full tools args = %q", got)
	}
	for _, tools := range []string{"", "safe", "ask", "edits"} {
		if got := openCodeModeArgs(tools); got != nil {
			t.Fatalf("%q tools should not add opencode permission flags, got %v", tools, got)
		}
	}
}

// Pins claude's permission-mode mapping (shared by openclaude).
func TestClaudeToolsArgs(t *testing.T) {
	if got := strings.Join(claudeToolsArgs("edits"), " "); got != "--permission-mode acceptEdits" {
		t.Errorf("edits → %q", got)
	}
	if got := strings.Join(claudeToolsArgs("full"), " "); got != "--permission-mode bypassPermissions" {
		t.Errorf("full → %q", got)
	}
	if got := claudeToolsArgs(""); got != nil {
		t.Errorf("safe default must add no flags, got %v", got)
	}
	// "ask" adds no permission-mode flag — the prompt tool governs.
	if got := claudeToolsArgs("ask"); got != nil {
		t.Errorf("ask must add no permission-mode flag, got %v", got)
	}
}

// Pins the "ask" wiring: a temp --mcp-config registering the shim plus
// --permission-prompt-tool, and nothing at other levels / without a
// provider.
func TestApprovalArgs(t *testing.T) {
	ap := &ApprovalConfig{Command: "/usr/bin/praimate-gui", Args: []string{"-mcp-approve", "http://127.0.0.1:9/approve/c1", "-mcp-token", "tok"}}

	args, cleanup, err := approvalArgs("ask", ap)
	if err != nil {
		t.Fatalf("approvalArgs: %v", err)
	}
	defer cleanup()
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--permission-prompt-tool mcp__praimate__approve") {
		t.Errorf("missing prompt tool flag: %q", joined)
	}
	if len(args) < 2 || args[0] != "--mcp-config" {
		t.Fatalf("missing --mcp-config: %v", args)
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		t.Fatalf("mcp config unreadable: %v", err)
	}
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("mcp config invalid json: %v", err)
	}
	srv, ok := cfg.MCPServers["praimate"]
	if !ok || srv.Command != ap.Command || len(srv.Args) != len(ap.Args) {
		t.Fatalf("mcp config = %+v", cfg)
	}
	cleanup()
	if _, err := os.Stat(args[1]); !os.IsNotExist(err) {
		t.Error("cleanup did not remove the temp mcp config")
	}

	for _, tc := range []struct {
		tools string
		ap    *ApprovalConfig
	}{{"", ap}, {"edits", ap}, {"full", ap}, {"ask", nil}} {
		args, cleanup, err := approvalArgs(tc.tools, tc.ap)
		if err != nil || len(args) != 0 {
			t.Errorf("tools=%q ap=%v: want no args, got %v err=%v", tc.tools, tc.ap != nil, args, err)
		}
		cleanup()
	}
}

func TestIsBatchShim(t *testing.T) {
	for path, want := range map[string]bool{
		`C:\Users\u\AppData\Local\pnpm\claude.CMD`: true,
		`C:\nodejs\gemini.cmd`:                     true,
		`C:\scripts\run.bat`:                       true,
		`C:\Program Files\claude\claude.exe`:       false,
		`/usr/local/bin/claude`:                    false,
	} {
		if got := isBatchShim(path); got != want {
			t.Errorf("isBatchShim(%q) = %v, want %v", path, got, want)
		}
	}
}

// Pins the managed-bin-dir fallback: praimate-code's standard install
// is NOT on PATH, so resolveBin must find it via extraDirs.
func TestExecAdapter_ResolvesFromExtraDirs(t *testing.T) {
	dir := t.TempDir()
	name := "praimate-code"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(dir, name)
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir()) // PATH cannot resolve it

	a := NewPraimateCodeAdapter()
	a.extraDirs = []string{dir}
	got, err := a.resolveBin()
	if err != nil {
		t.Fatalf("resolveBin: %v", err)
	}
	if got != bin {
		t.Fatalf("resolveBin = %q, want %q", got, bin)
	}
}

func TestRegisterAllCLIAdapters_RegistersSupportedCLIs(t *testing.T) {
	RegisterAllCLIAdapters()
	for _, name := range []string{"claude", "openclaude", "codex", "opencode", "praimate-code"} {
		if _, err := GetCLIAdapter(name); err != nil {
			t.Errorf("adapter %q not registered: %v", name, err)
		}
	}
}
