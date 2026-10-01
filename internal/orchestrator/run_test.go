package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

type fakeWorker struct {
	responses []string
	requests  []workerruntime.Request
}

func (*fakeWorker) ID() string { return "fake" }
func (*fakeWorker) Capabilities() workerruntime.Capabilities {
	return workerruntime.Capabilities{ReadOnly: true}
}
func (f *fakeWorker) Execute(_ context.Context, req workerruntime.Request) (*workerruntime.Result, error) {
	f.requests = append(f.requests, req)
	if len(f.responses) == 0 {
		return nil, errors.New("unexpected worker call")
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return &workerruntime.Result{Content: response}, nil
}

func testConfig(t *testing.T) Config {
	t.Helper()
	profiles := make([]Profile, 0, 3)
	for _, tier := range []Tier{Primary, Middle, Fast} {
		profiles = append(profiles, Profile{Tier: tier, Runtime: "native", Model: "model-" + string(tier), Endpoint: "http://localhost:11434/v1", TimeoutSeconds: 10, MaxInputBytes: 16384, MaxOutputTokens: 100})
	}
	return Config{Workspace: t.TempDir(), Profiles: profiles}
}

func TestDirectDelegationStartsAtSelectedTier(t *testing.T) {
	for _, tier := range []Tier{Middle, Fast} {
		t.Run(string(tier), func(t *testing.T) {
			worker := &fakeWorker{responses: []string{`{"action":"final","content":"scoped result"}`}}
			resolved := []Tier{}
			runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) {
				resolved = append(resolved, p.Tier)
				return worker, nil
			}}
			result, err := runner.RunFromTier(context.Background(), testConfig(t), tier, "small scoped task")
			if err != nil || result != "scoped result" || len(resolved) != 1 || resolved[0] != tier {
				t.Fatalf("result %q err %v resolved %v", result, err, resolved)
			}
			if worker.requests[0].Task != "small scoped task" {
				t.Fatal("unexpected task context")
			}
		})
	}
}

func TestStoppedManagerRejectsNewExecutions(t *testing.T) {
	m := NewManager(nil, context.Background())
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.startWithTier("task", "", testConfig(t), Fast, nil); err == nil {
		t.Fatal("stopped manager accepted execution")
	}
}

func TestNestedDelegationAndContextIsolation(t *testing.T) {
	workers := map[Tier]*fakeWorker{
		Primary: {responses: []string{`{"action":"delegate","tier":"middle","task":"analyze the small issue"}`, `{"action":"final","content":"done"}`}},
		Middle:  {responses: []string{`{"action":"delegate","tier":"fast","task":"find a symbol"}`, `{"action":"final","content":"middle result"}`}},
		Fast:    {responses: []string{`{"action":"final","content":"fast result"}`}},
	}
	var events []Event
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) { return workers[p.Tier], nil }, Emit: func(e Event) { events = append(events, e) }}
	result, err := runner.Run(context.Background(), testConfig(t), "original private request")
	if err != nil || result != "done" {
		t.Fatalf("result=%q error=%v", result, err)
	}
	if len(workers[Fast].requests) != 1 || workers[Fast].requests[0].Task != "find a symbol" {
		t.Fatalf("fast context leaked: %+v", workers[Fast].requests)
	}
	for tier, worker := range workers {
		if worker.requests[0].Model != "model-"+string(tier) {
			t.Fatalf("%s received model %q", tier, worker.requests[0].Model)
		}
	}
	if strings.Contains(workers[Middle].requests[0].Task, "original private request") {
		t.Fatalf("middle received primary context")
	}
	if len(events) == 0 || events[len(events)-1].Kind != "response" {
		t.Fatalf("missing final event: %+v", events)
	}
}

func TestWorkerRecoversFromProtocolAndReadErrors(t *testing.T) {
	for _, first := range []string{"I should inspect the file first.", `{"action":"tool","tool":"project.read","arguments":{"path":"missing.go"}}`} {
		worker := &fakeWorker{responses: []string{first, "```json\n{\"action\":\"final\",\"content\":\"reported accurately\"}\n```"}}
		runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
		result, err := runner.Run(context.Background(), testConfig(t), "inspect task")
		if err != nil || result != "reported accurately" || len(worker.requests) != 2 {
			t.Fatalf("result=%s err=%v", result, err)
		}
		if !strings.Contains(worker.requests[1].Task, "Invalid response") && !strings.Contains(worker.requests[1].Task, "failed") {
			t.Fatal("worker did not receive failure evidence")
		}
	}
}

func TestWorkerChildFailureReturnsToParent(t *testing.T) {
	workers := map[Tier]*fakeWorker{
		Primary: {responses: []string{`{"action":"delegate","tier":"fast","task":"inspect task"}`, `{"action":"final","content":"child failed; no success claimed"}`}},
		Fast:    {responses: []string{"malformed", "still malformed"}},
	}
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) { return workers[p.Tier], nil }}
	result, err := runner.Run(context.Background(), testConfig(t), "task")
	if err != nil || !strings.Contains(result, "child failed") {
		t.Fatalf("result=%s err=%v", result, err)
	}
	if !strings.Contains(workers[Primary].requests[1].Task, "task failed") {
		t.Fatal("parent lost child failure")
	}
}

func TestWorkerTaskCanCompleteAfterSixActions(t *testing.T) {
	worker := &fakeWorker{}
	for range 7 {
		worker.responses = append(worker.responses, `{"action":"list","path":"."}`)
	}
	worker.responses = append(worker.responses, `{"action":"final","content":"finished"}`)
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
	if result, err := runner.Run(context.Background(), testConfig(t), "inspect"); err != nil || result != "finished" {
		t.Fatalf("result=%s err=%v", result, err)
	}
}

func TestWorkerResumeEnforcesConcurrencyLimit(t *testing.T) {
	m := &Manager{runs: map[string]*Run{}}
	for _, id := range []string{"a", "b", "c"} {
		m.runs[id] = &Run{Status: "running", cancel: func() {}}
	}
	m.runs["saved"] = &Run{Status: "completed"}
	if err := m.Continue("saved", "continue"); err == nil || !strings.Contains(err.Error(), "three") {
		t.Fatalf("resume bypassed concurrency limit: %v", err)
	}
}

func TestNestedWorkersApplyAndVerifyChange(t *testing.T) {
	config := testConfig(t)
	config.Profiles[2].AllowEdits = true
	path := filepath.Join(config.Workspace, "main.go")
	if err := os.WriteFile(path, []byte("package old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	workers := map[Tier]*fakeWorker{
		Primary: {responses: []string{`{"action":"delegate","tier":"middle","task":"fix and verify main.go"}`, `{"action":"final","content":"integrated verified change"}`}},
		Middle:  {responses: []string{`{"action":"delegate","tier":"fast","task":"replace package old with package fixed in main.go"}`, `{"action":"inspect","path":"main.go"}`, `{"action":"final","content":"verified main.go contains package fixed"}`}},
		Fast:    {responses: []string{`{"action":"replace","path":"main.go","oldText":"package old","newText":"package fixed"}`, `{"action":"final","content":"edited main.go"}`}},
	}
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) { return workers[p.Tier], nil }}
	result, err := runner.Run(context.Background(), config, "coordinate a fix")
	if err != nil || result != "integrated verified change" {
		t.Fatalf("result=%s err=%v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package fixed\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if !strings.Contains(workers[Middle].requests[2].Task, "package fixed") || !strings.Contains(workers[Primary].requests[1].Task, "verified main.go") {
		t.Fatal("verification evidence did not reach parent")
	}
}

func TestWorkerRunSharesRequestBudgetAcrossDelegations(t *testing.T) {
	primary, fast := &fakeWorker{}, &fakeWorker{}
	for range 16 {
		primary.responses = append(primary.responses, `{"action":"delegate","tier":"fast","task":"inspect"}`)
		for range 15 {
			fast.responses = append(fast.responses, `{"action":"list","path":"."}`)
		}
		fast.responses = append(fast.responses, `{"action":"final","content":"inspected"}`)
	}
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) {
		if p.Tier == Primary {
			return primary, nil
		}
		return fast, nil
	}}
	_, err := runner.Run(context.Background(), testConfig(t), "inspect")
	if err == nil || !strings.Contains(err.Error(), "64 worker requests") || len(primary.requests)+len(fast.requests) != 64 {
		t.Fatalf("shared budget not enforced: primary=%d fast=%d err=%v", len(primary.requests), len(fast.requests), err)
	}
}

func TestSameCLIProfilesKeepIndependentModels(t *testing.T) {
	config := testConfig(t)
	for i := range config.Profiles {
		config.Profiles[i].Runtime = "cli"
		config.Profiles[i].CLI = "codex"
		config.Profiles[i].Endpoint = ""
		config.Profiles[i].MaxOutputTokens = 0
	}
	config.Profiles[0].Model = "reasoning-model"
	config.Profiles[1].Model = "medium-model"
	worker := &fakeWorker{responses: []string{
		`{"action":"delegate","tier":"middle","task":"small task"}`,
		`{"action":"final","content":"middle result"}`,
		`{"action":"final","content":"primary result"}`,
	}}
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
	result, err := runner.Run(context.Background(), config, "main task")
	if err != nil || result != "primary result" {
		t.Fatalf("result=%q error=%v", result, err)
	}
	want := []string{"reasoning-model", "medium-model", "reasoning-model"}
	if len(worker.requests) != len(want) {
		t.Fatalf("calls=%d, want %d", len(worker.requests), len(want))
	}
	for i, req := range worker.requests {
		if req.Model != want[i] {
			t.Fatalf("call %d model=%q, want %q", i, req.Model, want[i])
		}
	}
}

func TestBuiltInWorkerAdaptersAreResolvable(t *testing.T) {
	core.RegisterAllCLIAdapters()
	for _, cli := range []string{"codex", "claude", "openclaude", "opencode", "praimate-code", "copilot"} {
		worker, err := ResolveRuntime(Profile{Runtime: "cli", CLI: cli})
		if err != nil || !worker.Capabilities().ReadOnly {
			t.Fatalf("CLI %s: worker=%v error=%v", cli, worker, err)
		}
	}
	workerWithHostEdits, err := ResolveRuntime(Profile{Runtime: "cli", CLI: "codex", AllowEdits: true, AllowCommands: true})
	if err != nil || !workerWithHostEdits.Capabilities().ReadOnly {
		t.Fatalf("CLI process did not remain read-only with host-managed tools: worker=%v error=%v", workerWithHostEdits, err)
	}
	worker, err := ResolveRuntimeWithCore(nil)(Profile{Runtime: "cli", CLI: "praimate-cli"})
	if err != nil || worker.ID() != "cli:praimate-cli" {
		t.Fatalf("praimate-cli: worker=%v error=%v", worker, err)
	}
}

func TestPrimaryReceivesMultipleDelegatedResults(t *testing.T) {
	workers := map[Tier]*fakeWorker{
		Primary: {responses: []string{`{"action":"delegate","tier":"middle","task":"first"}`, `{"action":"delegate","tier":"fast","task":"second"}`, `{"action":"final","content":"integrated"}`}},
		Middle:  {responses: []string{`{"action":"final","content":"middle evidence"}`}},
		Fast:    {responses: []string{`{"action":"final","content":"fast evidence"}`}},
	}
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) { return workers[p.Tier], nil }}
	result, err := runner.Run(context.Background(), testConfig(t), "integrate results")
	if err != nil || result != "integrated" {
		t.Fatalf("result=%q error=%v", result, err)
	}
	last := workers[Primary].requests[2].Task
	if !strings.Contains(last, "middle evidence") || !strings.Contains(last, "fast evidence") {
		t.Fatalf("primary lost delegated results: %s", last)
	}
}

func TestRejectsInvalidDelegation(t *testing.T) {
	worker := &fakeWorker{responses: []string{`{"action":"delegate","tier":"primary","task":"loop"}`}}
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
	_, err := runner.Run(context.Background(), testConfig(t), "task")
	if err == nil || !strings.Contains(err.Error(), "cannot delegate") {
		t.Fatalf("expected rejected delegation, got %v", err)
	}
}

func TestRejectsMalformedWorkerOutput(t *testing.T) {
	for _, response := range []string{`{"action":"final","content":"ok","unknown":1}`, `{"action":"final","content":"ok"} {}`, ""} {
		t.Run(response, func(t *testing.T) {
			worker := &fakeWorker{responses: []string{response}}
			runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
			if _, err := runner.Run(context.Background(), testConfig(t), "task"); err == nil {
				t.Fatal("expected malformed response to fail")
			}
		})
	}
}

func TestNativeWorkerInspectsBoundedSource(t *testing.T) {
	config := testConfig(t)
	if err := os.WriteFile(filepath.Join(config.Workspace, "main.go"), []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	worker := &fakeWorker{responses: []string{`{"action":"list","path":"."}`, `{"action":"inspect","path":"main.go"}`, `{"action":"final","content":"saw package main"}`}}
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
	result, err := runner.Run(context.Background(), config, "inspect main.go")
	if err != nil || result != "saw package main" || len(worker.requests) != 3 || !strings.Contains(worker.requests[1].Task, "main.go") || !strings.Contains(worker.requests[2].Task, "package main") {
		t.Fatalf("inspection result=%q err=%v requests=%+v", result, err, worker.requests)
	}
}

func TestInspectRejectsSensitiveOrEscapingPaths(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "source.go")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"../source.go", ".env", "link.go"} {
		if _, err := inspectSource(root, path); err == nil {
			t.Fatalf("accepted %q", path)
		}
		if _, err := listSource(root, path); err == nil {
			t.Fatalf("listed %q", path)
		}
	}
}

func TestNativeExactReplacementRequiresProfilePermission(t *testing.T) {
	config := testConfig(t)
	path := filepath.Join(config.Workspace, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	response := `{"action":"replace","path":"main.go","oldText":"package main","newText":"package worker"}`
	worker := &fakeWorker{responses: []string{response}}
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }}
	if _, err := runner.Run(context.Background(), config, "edit"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected permission rejection, got %v", err)
	}
	config.Profiles[0].AllowEdits = true
	worker.responses = []string{response, `{"action":"final","content":"edited"}`}
	result, err := runner.Run(context.Background(), config, "edit")
	if err != nil || result != "edited" {
		t.Fatalf("replacement result=%q error=%v", result, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "package worker\n" {
		t.Fatalf("file=%q error=%v", data, err)
	}
}

func TestReplacementRejectsAmbiguousText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("x x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := replaceSource(root, "main.go", "x", "y"); err == nil {
		t.Fatal("accepted ambiguous replacement")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "x x" {
		t.Fatalf("file changed: %q", data)
	}
}

func TestWorkerCommandUsesManagedApproval(t *testing.T) {
	config := testConfig(t)
	config.Profiles[0].AllowCommands = true
	command := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		command += ".exe"
	}
	request, err := json.Marshal(map[string]any{"action": "tool", "tool": "command.run", "arguments": map[string]any{"command": command, "args": []string{"version"}}})
	if err != nil {
		t.Fatal(err)
	}
	worker := &fakeWorker{responses: []string{string(request), `{"action":"final","content":"version checked"}`}}
	approved := 0
	runner := Runner{Resolve: func(Profile) (workerruntime.Runtime, error) { return worker, nil }, Approval: &core.ApprovalConfig{Request: func(_ context.Context, name string, _ map[string]any) (bool, error) {
		if name != "command.run" {
			t.Fatalf("unexpected approval tool %q", name)
		}
		approved++
		return true, nil
	}}}
	result, err := runner.Run(context.Background(), config, "check compiler")
	if err != nil || result != "version checked" || approved != 1 {
		t.Fatalf("result=%q error=%v approvals=%d", result, err, approved)
	}
	if len(worker.requests) != 2 || !strings.Contains(worker.requests[1].Task, "go version") {
		t.Fatalf("command output was not returned to worker: %+v", worker.requests)
	}
	config.Profiles[0].AllowCommands = false
	worker = &fakeWorker{responses: []string{string(request)}}
	runner.Resolve = func(Profile) (workerruntime.Runtime, error) { return worker, nil }
	_, err = runner.Run(context.Background(), config, "check compiler")
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("command without profile permission: %v", err)
	}
}

func TestRoutingInstructionsDescribeChildModelsAndOutput(t *testing.T) {
	config := testConfig(t)
	config.Profiles[1].CLI = "claude"
	config.Profiles[1].Runtime = "cli"
	config.Profiles[2].CLI = "praimate-code"
	config.Profiles[2].Runtime = "cli"
	primary := workerInstructions(config, Primary, config.Profiles[0])
	if !strings.Contains(primary, "model-middle") || !strings.Contains(primary, "model-fast") || !strings.Contains(primary, `"action":"delegate"`) || !strings.Contains(primary, "concise") {
		t.Fatalf("primary routing prompt incomplete: %s", primary)
	}
	fast := workerInstructions(config, Fast, config.Profiles[2])
	if strings.Contains(fast, `"action":"delegate"`) || !strings.Contains(fast, "parent") {
		t.Fatalf("fast output contract incomplete: %s", fast)
	}
}

func TestEvidenceKeepsRecentResultsWithinBudget(t *testing.T) {
	input := workerInputWithEvidence("task", []string{"old", "middle finding", "fast finding"}, 1024)
	if !strings.Contains(input, "middle finding") || !strings.Contains(input, "fast finding") || !strings.Contains(input, "task") {
		t.Fatalf("missing observations: %s", input)
	}
	bounded := workerInputWithEvidence("task", []string{strings.Repeat("x", 20000)}, 512)
	if len(bounded) > 512 || !strings.Contains(bounded, "xxx") {
		t.Fatalf("evidence not bounded: len=%d", len(bounded))
	}
}
