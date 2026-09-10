package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

type externalWorkflowTestAdapter struct {
	t        *testing.T
	calls    int
	checkMCP bool
}

type externalTimeoutTestAdapter struct{}

func (externalTimeoutTestAdapter) Name() string                    { return "praimate-code" }
func (externalTimeoutTestAdapter) Available(context.Context) error { return nil }
func (externalTimeoutTestAdapter) SupportsResume() bool            { return false }
func (externalTimeoutTestAdapter) ManagedSafeMode() bool           { return true }
func (externalTimeoutTestAdapter) SingleShot(ctx context.Context, _ core.SingleShotOpts) (*core.Reply, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (a externalTimeoutTestAdapter) SingleShotStream(ctx context.Context, opts core.SingleShotOpts, _ core.StreamHandler) (*core.Reply, error) {
	return a.SingleShot(ctx, opts)
}
func (externalTimeoutTestAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("resume unsupported")
}
func (externalTimeoutTestAdapter) ResumeStream(context.Context, string, core.ResumeOpts, core.StreamHandler) (*core.Reply, error) {
	return nil, errors.New("resume unsupported")
}

func (a *externalWorkflowTestAdapter) Name() string                    { return "praimate-code" }
func (a *externalWorkflowTestAdapter) Available(context.Context) error { return nil }
func (a *externalWorkflowTestAdapter) SupportsResume() bool            { return true }
func (a *externalWorkflowTestAdapter) ManagedSafeMode() bool           { return true }
func (a *externalWorkflowTestAdapter) SingleShot(ctx context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	return a.SingleShotStream(ctx, opts, nil)
}
func (a *externalWorkflowTestAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("unexpected resume")
}
func (a *externalWorkflowTestAdapter) SingleShotStream(_ context.Context, opts core.SingleShotOpts, emit core.StreamHandler) (*core.Reply, error) {
	a.calls++
	if a.checkMCP {
		raw, err := os.ReadFile(filepath.Join(opts.Cwd, "opencode.json"))
		if err != nil || !strings.Contains(string(raw), `"docx-local"`) {
			a.t.Fatalf("external run did not prepare agent MCP config: err=%v body=%s", err, raw)
		}
	}
	if emit != nil {
		emit(core.StreamEvent{Type: "tool_start", Tool: "docx_local_ping", ID: "tool-42", Detail: "{}"})
		emit(core.StreamEvent{Type: "tool_end", Tool: "docx_local_ping", ID: "tool-42", OK: true})
	}
	return &core.Reply{Text: "workflow complete", SessionID: "session-1"}, nil
}
func (a *externalWorkflowTestAdapter) ResumeStream(context.Context, string, core.ResumeOpts, core.StreamHandler) (*core.Reply, error) {
	return nil, errors.New("unexpected resume")
}

type agentPromptTestAdapter struct {
	t             *testing.T
	expectedModel string
	expectedURL   string
	expectedToken string
}

func (agentPromptTestAdapter) Name() string                    { return "openclaude" }
func (agentPromptTestAdapter) Available(context.Context) error { return nil }
func (agentPromptTestAdapter) SupportsResume() bool            { return false }
func (agentPromptTestAdapter) Resume(context.Context, string, core.ResumeOpts) (*core.Reply, error) {
	return nil, errors.New("resume unsupported")
}
func (a agentPromptTestAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	if opts.Tools != "" {
		a.t.Fatalf("headless Safe inherited wider tools policy %q", opts.Tools)
	}
	if opts.Model != a.expectedModel {
		a.t.Fatalf("model = %q, want %q", opts.Model, a.expectedModel)
	}
	if opts.Env["OPENAI_BASE_URL"] != a.expectedURL {
		a.t.Fatalf("endpoint = %q, want %q", opts.Env["OPENAI_BASE_URL"], a.expectedURL)
	}
	if opts.Env["OPENAI_API_KEY"] != a.expectedToken {
		a.t.Fatalf("endpoint token = %q, want encrypted setting", opts.Env["OPENAI_API_KEY"])
	}
	return &core.Reply{Text: "reviewed: " + opts.Message, ExitCode: 0}, nil
}

func TestRunAgentPromptProducesStableJSONAndRemovesTemporaryChat(t *testing.T) {
	root := t.TempDir()
	// Execution prepares native CLI configuration independently of PRAIMATE_HOME.
	// Keep the fake adapter test away from the user's real OpenClaude profile.
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	t.Setenv("PRAIMATE_HOME", root)
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://llm.example/v1"}); err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSetting(context.Background(), core.ScopeCLI, "local_llm.api_key", []byte(`"db-secret"`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportAgentYAML(context.Background(), []byte(`
schema: praimate.agent/v1
id: dev-team
name: Dev Team
description: Reviews code
instructions: Review carefully.
supports: [codex, claude, openclaude]
tools: []
mcp_servers: []
workflows: []
surfaces: [chat]
`), ""); err != nil {
		t.Fatal(err)
	}
	if err := core.SaveAgentRuntime("dev-team", &core.AgentRuntimeManifest{
		Schema: core.AgentRuntimeSchema, Mode: core.RuntimeNative,
		Permissions: core.AgentRuntimePermissions{DefaultTools: "edits"},
	}); err != nil {
		t.Fatal(err)
	}
	core.RegisterCLIAdapter(agentPromptTestAdapter{
		t: t, expectedModel: "qwen3-coder", expectedURL: "https://llm.example/v1", expectedToken: "db-secret",
	})
	t.Cleanup(func() { core.RegisterCLIAdapter(core.NewOpenClaudeAdapter()) })

	oldOpen, oldInput, oldOutput, oldError := openAgentRunCore, agentRunInput, agentRunOutput, agentRunError
	oldReadPassword, oldTerminalAvailable := readAgentRunTerminalPassword, agentRunTerminalAvailable
	t.Cleanup(func() {
		openAgentRunCore, agentRunInput, agentRunOutput, agentRunError = oldOpen, oldInput, oldOutput, oldError
		readAgentRunTerminalPassword, agentRunTerminalAvailable = oldReadPassword, oldTerminalAvailable
	})
	var openPasswords []string
	openAgentRunCore = func(password string) (*core.Core, func(), error) {
		openPasswords = append(openPasswords, password)
		if password == "" {
			return nil, nil, store.ErrPasswordRequired
		}
		if password != "correct horse battery staple" {
			return nil, nil, errors.New("unexpected password")
		}
		return c, func() {}, nil
	}
	agentRunTerminalAvailable = func() bool { return true }
	readAgentRunTerminalPassword = func() (string, error) {
		return "correct horse battery staple", nil
	}
	var stdout, stderr bytes.Buffer
	agentRunInput, agentRunOutput, agentRunError = bytes.NewBuffer(nil), &stdout, &stderr

	code := runAgentPrompt(agentPromptOptions{
		AgentID: "dev-team", Folder: root, Prompt: "review this code",
		CLI: "openclaude", Model: "qwen3-coder", Endpoint: "saved",
		Output: "json", Tools: "safe", SkipModelPreflight: true,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var got agentRunResult
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, stdout.String())
	}
	if !got.OK || got.Schema != agentRunSchema || got.AgentID != "dev-team" || got.CLI != "openclaude" || got.Reply != "reviewed: review this code" {
		t.Fatalf("unexpected result: %#v", got)
	}
	if got.ChatID != "" {
		t.Fatalf("temporary run exposed chat id %q", got.ChatID)
	}
	chats, err := c.ListChats(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(chats) != 0 {
		t.Fatalf("temporary chats retained: %#v", chats)
	}
	if len(openPasswords) != 2 || openPasswords[0] != "" || openPasswords[1] != "correct horse battery staple" {
		t.Fatalf("database open passwords = %#v, want remembered lookup then hidden terminal password", openPasswords)
	}

	stdout.Reset()
	stderr.Reset()
	code = runAgentPrompt(agentPromptOptions{
		AgentID: "dev-team", Folder: root, Prompt: "review this code",
		CLI: "claude", Model: "qwen3-coder", Endpoint: "saved",
		Output: "json", Tools: "safe", SkipModelPreflight: true,
	})
	if code != 1 {
		t.Fatalf("Claude local exit = %d, stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var rejected agentRunResult
	if err := json.Unmarshal(stdout.Bytes(), &rejected); err != nil {
		t.Fatalf("invalid rejection JSON: %v\n%s", err, stdout.String())
	}
	if rejected.OK || !strings.Contains(rejected.Error, "OpenClaude") {
		t.Fatalf("Claude local rejection = %#v", rejected)
	}
}

func TestReadAgentPromptRejectsDualSources(t *testing.T) {
	if _, err := readAgentPrompt(agentPromptOptions{Prompt: "inline", PromptFile: "prompt.txt"}); err == nil {
		t.Fatal("expected dual prompt sources to fail")
	}
}

func TestResolveAgentEndpointRejectsCredentialRedirect(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "https://trusted.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveAgentEndpoint("https://attacker.example/v1"); err == nil {
		t.Fatal("expected an endpoint different from the saved endpoint to be rejected")
	}
	if got, err := resolveAgentEndpoint("saved"); err != nil || got != "https://trusted.example/v1" {
		t.Fatalf("resolve saved endpoint = %q, %v", got, err)
	}
}

func TestWriteAgentFailureUsesResultEnvelopeForJSONL(t *testing.T) {
	oldOutput := agentRunOutput
	t.Cleanup(func() { agentRunOutput = oldOutput })
	var stdout bytes.Buffer
	agentRunOutput = &stdout

	if code := writeAgentFailure(agentPromptOptions{AgentID: "dev-team", Output: "jsonl"}, 2, "bad input"); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	var got struct {
		Type  string `json:"type"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSONL result: %v", err)
	}
	if got.Type != "result" || got.OK || got.Error != "bad input" {
		t.Fatalf("failure envelope = %#v", got)
	}
}

func TestRunAgentWorkflowJSONLPreparesMCPAndReplaysDurableResult(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddCustomMCP(context.Background(), core.AddCustomMCPRequest{
		ID: "docx-local", Name: "DOCX local", Transport: string(core.MCPTransportStdio),
		Command: "python", Args: []string{"docx_mcp.py"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportAgentYAML(context.Background(), []byte(`
schema: praimate.agent/v1
id: docx-worker
name: DOCX Worker
description: rearranges documents
instructions: Work safely.
supports: [praimate-code]
tools: []
mcp_servers: [docx-local]
workflows:
  - name: Rearrange
    inputs:
      - name: source
        required: true
    steps:
      - kind: user_message
        template: "Inspect {{ .source }}"
surfaces: [chat]
`), ""); err != nil {
		t.Fatal(err)
	}
	adapter := &externalWorkflowTestAdapter{t: t, checkMCP: true}
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() { core.RegisterCLIAdapter(core.NewPraimateCodeAdapter()) })

	oldOpen, oldOutput, oldError := openAgentRunCore, agentRunOutput, agentRunError
	t.Cleanup(func() { openAgentRunCore, agentRunOutput, agentRunError = oldOpen, oldOutput, oldError })
	openAgentRunCore = func(password string) (*core.Core, func(), error) { return c, func() {}, nil }
	var stdout, stderr bytes.Buffer
	agentRunOutput, agentRunError = &stdout, &stderr
	opts := agentPromptOptions{
		AgentID: "docx-worker", CLI: "praimate-code", Folder: root,
		Workflow: "Rearrange", Inputs: []string{"source=/input/a.docx"},
		Tools: "full", Output: "jsonl", RunID: "controller-job-42",
	}
	if code := runAgentPrompt(opts); code != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var events []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("invalid JSONL: %v\n%s", err, scanner.Text())
		}
		events = append(events, event)
	}
	if len(events) < 3 || events[len(events)-1]["type"] != "result" {
		t.Fatalf("events=%#v", events)
	}
	var correlated bool
	for _, event := range events {
		if event["eventType"] == "tool_start" && event["id"] == "tool-42" && event["runId"] == "controller-job-42" {
			correlated = true
		}
	}
	if !correlated {
		t.Fatalf("tool event lost correlation: %#v", events)
	}
	if adapter.calls != 1 {
		t.Fatalf("adapter calls=%d", adapter.calls)
	}

	stdout.Reset()
	if code := runAgentPrompt(opts); code != 0 {
		t.Fatalf("cached exit=%d stdout=%s", code, stdout.String())
	}
	if adapter.calls != 1 {
		t.Fatalf("cached run executed adapter again: calls=%d", adapter.calls)
	}
	var cached map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &cached); err != nil || cached["cached"] != true {
		t.Fatalf("cached result=%#v err=%v body=%s", cached, err, stdout.String())
	}

	stdout.Reset()
	if code := runAgentStatus(agentStatusOptions{RunID: opts.RunID, Output: "json"}); code != 0 {
		t.Fatalf("status exit=%d body=%s", code, stdout.String())
	}
	var status agentStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil || status.State != "completed" || status.Attempt != 1 || len(status.Result) == 0 {
		t.Fatalf("status=%#v err=%v", status, err)
	}

	stdout.Reset()
	opts.Retry = true
	if code := runAgentPrompt(opts); code != 0 || adapter.calls != 2 {
		t.Fatalf("retry exit=%d calls=%d body=%s", code, adapter.calls, stdout.String())
	}
	var retryResult agentRunResult
	retryLines := bytes.Split(bytes.TrimSpace(stdout.Bytes()), []byte("\n"))
	if err := json.Unmarshal(retryLines[len(retryLines)-1], &retryResult); err != nil || retryResult.Attempt != 2 {
		t.Fatalf("retry result=%#v err=%v body=%s", retryResult, err, stdout.String())
	}
}

func TestRunAgentWorkflowTimeoutIsDurableAndReturns124(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportAgentYAML(context.Background(), []byte(`
schema: praimate.agent/v1
id: slow-worker
name: Slow Worker
description: timeout fixture
instructions: Wait.
supports: [praimate-code]
tools: []
mcp_servers: []
workflows:
  - name: Wait
    steps:
      - kind: user_message
        template: "wait"
surfaces: [chat]
`), ""); err != nil {
		t.Fatal(err)
	}
	core.RegisterCLIAdapter(externalTimeoutTestAdapter{})
	t.Cleanup(func() { core.RegisterCLIAdapter(core.NewPraimateCodeAdapter()) })

	oldOpen, oldOutput, oldError := openAgentRunCore, agentRunOutput, agentRunError
	t.Cleanup(func() { openAgentRunCore, agentRunOutput, agentRunError = oldOpen, oldOutput, oldError })
	openAgentRunCore = func(password string) (*core.Core, func(), error) { return c, func() {}, nil }
	var stdout, stderr bytes.Buffer
	agentRunOutput, agentRunError = &stdout, &stderr

	opts := agentPromptOptions{
		AgentID: "slow-worker", CLI: "praimate-code", Folder: root, Workflow: "Wait",
		Tools: "safe", Output: "json", RunID: "controller-timeout-42", Timeout: 10 * time.Millisecond,
	}
	if code := runAgentPrompt(opts); code != 124 {
		t.Fatalf("exit=%d, want 124; stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	var result agentRunResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.State != "timed_out" || result.ExitCode != 124 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	run, err := c.GetExternalAgentRun(context.Background(), opts.RunID)
	if err != nil || run.State != "timed_out" || run.ResultJSON == "" {
		t.Fatalf("durable run=%#v err=%v", run, err)
	}
}

func TestParseStrictInputsRejectsMalformedAndDuplicates(t *testing.T) {
	if _, err := parseStrictInputs([]string{"missing-separator"}); err == nil {
		t.Fatal("expected malformed input to fail")
	}
	if _, err := parseStrictInputs([]string{"x=1", "x=2"}); err == nil {
		t.Fatal("expected duplicate input to fail")
	}
	got, err := parseStrictInputs([]string{"source=/a,b.docx", "goal=keep tables"})
	if err != nil || got["source"] != "/a,b.docx" || got["goal"] != "keep tables" {
		t.Fatalf("inputs=%v err=%v", got, err)
	}
}

func TestExternalRunRequestHashIncludesExecutionControls(t *testing.T) {
	base, err := externalRunRequestHash("agent", "praimate-code", "/tmp/project", "prompt", "review", "", nil, "model", "https://llm/v1", "", time.Minute, false, false)
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		timeout time.Duration
		persist bool
		skip    bool
	}{
		"timeout":   {timeout: 2 * time.Minute},
		"persist":   {timeout: time.Minute, persist: true},
		"preflight": {timeout: time.Minute, skip: true},
	} {
		got, err := externalRunRequestHash("agent", "praimate-code", "/tmp/project", "prompt", "review", "", nil, "model", "https://llm/v1", "", tc.timeout, tc.persist, tc.skip)
		if err != nil {
			t.Fatal(err)
		}
		if got == base {
			t.Errorf("%s control did not change request hash", name)
		}
	}
}

func TestRunModelCheckUsesStoredCredentialAndOptionalProbe(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		switch r.URL.Path {
		case "/api/tags":
			http.NotFound(w, r)
		case "/v1/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "qwen-test"}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: server.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	if err := c.SetSetting(context.Background(), core.ScopeCLI, "local_llm.api_key", []byte(`"secret"`)); err != nil {
		t.Fatal(err)
	}
	adapter := &externalWorkflowTestAdapter{t: t}
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() { core.RegisterCLIAdapter(core.NewPraimateCodeAdapter()) })
	oldOpen, oldOutput := openAgentRunCore, agentRunOutput
	t.Cleanup(func() { openAgentRunCore, agentRunOutput = oldOpen, oldOutput })
	openAgentRunCore = func(password string) (*core.Core, func(), error) { return c, func() {}, nil }
	var stdout bytes.Buffer
	agentRunOutput = &stdout
	if code := runModelCheck(modelCheckOptions{
		CLI: "praimate-code", Folder: root, Endpoint: "saved", Model: "qwen-test",
		Output: "json", Probe: true,
	}); code != 0 {
		t.Fatalf("exit=%d body=%s", code, stdout.String())
	}
	var result modelCheckResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || !result.Present || !result.Responding || !result.OK {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if auth != "Bearer secret" {
		t.Fatalf("Authorization=%q", auth)
	}
}
