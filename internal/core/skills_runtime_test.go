package core

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/agentic"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func TestStaticSkillTransportRejectsRequiredDynamicAndDiagnosesOptional(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	approveRuntimeFixture(t, version)
	agent.Skills.Bindings[0].Activation = "auto"
	settings := ChatSettings{SkillsV2: agent.Skills, SkillsLock: agent.SkillsLock}
	if _, _, err := c.BuildChatSkillPayload(context.Background(), settings, "test"); err == nil || !strings.Contains(err.Error(), "incompatible_transport") {
		t.Fatal("static chat advertised a broker it cannot provide", err)
	}
	agent.Skills.Bindings[0].Optional = true
	payload, state, err := c.BuildChatSkillPayload(context.Background(), settings, "test")
	if err != nil || payload != "" || state == nil || len(state.Diagnostics) != 1 {
		t.Fatal("optional dynamic binding not diagnosed", payload, state, err)
	}
}

func TestControlledSkillRuntimeCapturesExactPinnedPayload(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	_ = c
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error { return tx.Approve(ctx, version.Ref, version.Digest, true) }); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	scope, err := skills.SelectionScope(agent.Skills, agent.SkillsLock)
	if err != nil {
		t.Fatal(err)
	}
	runtime, plan, err := ControlledSkillRuntime(ctx, skills.SkillScopes{Agent: scope}, skills.ContextBudget{InputLimit: 16000, ModelWindow: 16000, ReservedOutput: 1000, SafetyMargin: 100, NonSkillInput: 20, Coverage: skills.CoverageControlledPayload, Measurement: skills.MeasurementEstimate})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	payload := ControlledPayload(plan)
	if !strings.Contains(payload, "EXACT INSTRUCTIONS") || !strings.Contains(payload, version.Digest) || plan.Receipt.Coverage != skills.CoverageControlledPayload {
		t.Fatalf("controlled payload missing evidence: %q %+v", payload, plan.Receipt)
	}
	if strings.Contains(payload, "allowed-tools") {
		t.Fatal("untrusted permission metadata was transported")
	}
}

func approveRuntimeFixture(t *testing.T, v skills.SkillVersion) {
	t.Helper()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Update(context.Background(), view.Revision, func(tx *skills.SkillHostTransaction) error {
		return tx.Approve(context.Background(), v.Ref, v.Digest, true)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSkillsLoadAndReadReachNextPayloadWithoutNativeDuplication(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	agent.Skills.Bindings[0].Activation = "auto"
	agent.Skills.Budget = forgeBudget()
	saveAutonomousRuntime(t, agent.ID)
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{
		fmt.Sprintf(`{"action":"tool","tool":"skill.load","arguments":{"ref":%q,"digest":%q}}`, v.Ref, v.Digest),
		fmt.Sprintf(`{"action":"tool","tool":"skill.read","arguments":{"ref":%q,"digest":%q,"path":"LICENSE","start":1,"lines":10}}`, v.Ref, v.Digest),
		`{"action":"finish","message":"done"}`,
	}}
	withMockAdapter(t, mock)
	var deliveries int
	result, err := c.RunManagedAgent(context.Background(), ManagedRunRequest{Surface: SurfaceStudio, Agent: agent, CLI: "claude", Cwd: t.TempDir(), Task: "review", OnEvent: func(e ManagedRunEvent) {
		if e.Type == "skills.delivered" {
			deliveries++
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "completed" || len(mock.shots) != 3 || len(mock.resumes) != 0 || deliveries != 3 {
		t.Fatalf("managed transport: result=%+v shots=%d resumes=%d receipts=%d", result, len(mock.shots), len(mock.resumes), deliveries)
	}
	if strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") {
		t.Fatal("auto body delivered before request")
	}
	if strings.Count(mock.shots[1].SystemPrompt, "EXACT INSTRUCTIONS") != 1 || strings.Contains(mock.shots[1].SystemPrompt, "TEST LICENSE") {
		t.Fatal("skill.load did not deliver only the requested body")
	}
	if strings.Count(mock.shots[2].SystemPrompt, "EXACT INSTRUCTIONS") != 1 || !strings.Contains(mock.shots[2].SystemPrompt, "TEST LICENSE") {
		t.Fatal("skill.read did not deliver resource")
	}
	for _, shot := range mock.shots {
		if strings.Contains(shot.Message, "EXACT INSTRUCTIONS") || strings.Contains(shot.Message, "TEST LICENSE") {
			t.Fatal("skill contents duplicated in runtime transcript")
		}
		if shot.Tools != "" {
			t.Fatal("skill elevated native tools")
		}
	}
}

func TestWorkflowSequenceUsesEachSelectionAndPersistsLastLock(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	empty := *agent.Skills
	empty.Bindings = []skills.SkillBinding{}
	empty.Lockfile = "skill-locks/second.json"
	emptyLock := *agent.SkillsLock
	emptyLock.Entries = []skills.SkillVersionLockEntry{}
	agent.Workflows = append(agent.Workflows, Workflow{Name: "second", Skills: &empty, SkillsLock: &emptyLock, Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "use previous result"}}})
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{"first result", "second result"}}
	withMockAdapter(t, mock)
	result := c.RunAllWorkflows(context.Background(), RunAllOptions{Agent: agent, CLI: "claude", Cwd: t.TempDir(), Persist: true})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(mock.shots) != 2 || len(mock.resumes) != 0 {
		t.Fatalf("different v2 scopes reused native context: shots=%d resumes=%d", len(mock.shots), len(mock.resumes))
	}
	if !strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") || strings.Contains(mock.shots[1].SystemPrompt, "EXACT INSTRUCTIONS") {
		t.Fatal("workflow retained the previous selected body")
	}
	if !strings.Contains(mock.shots[1].Message, "first result") {
		t.Fatal("workflow sequence lost previous result")
	}
	chat, err := c.GetChat(context.Background(), result.ChatID)
	if err != nil || len(chat.Settings.SkillsV2.Bindings) != 0 {
		t.Fatalf("continued workflow chat has wrong lock: %+v %v", chat, err)
	}
}

func TestManagedWorkflowSequenceUsesEachSelectionAndPersistsReceipt(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	saveAutonomousRuntime(t, agent.ID)
	empty := *agent.Skills
	empty.Bindings = []skills.SkillBinding{}
	empty.Lockfile = "skill-locks/second.json"
	emptyLock := *agent.SkillsLock
	emptyLock.Entries = []skills.SkillVersionLockEntry{}
	agent.Workflows = append(agent.Workflows, Workflow{Name: "second", Skills: &empty, SkillsLock: &emptyLock, Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "use previous result"}}})
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{`{"action":"finish","message":"first result"}`, `{"action":"finish","message":"second result"}`}}
	withMockAdapter(t, mock)
	result := c.RunAllWorkflows(context.Background(), RunAllOptions{Agent: agent, CLI: "claude", Cwd: t.TempDir(), Persist: true})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(mock.shots) != 2 || len(mock.resumes) != 0 {
		t.Fatalf("managed workflow scopes: shots=%d resumes=%d", len(mock.shots), len(mock.resumes))
	}
	if !strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") || strings.Contains(mock.shots[1].SystemPrompt, "EXACT INSTRUCTIONS") || !strings.Contains(mock.shots[1].Message, "first result") {
		t.Fatal("managed workflow did not replace selection and retain task results")
	}
	chat, err := c.GetChat(context.Background(), result.ChatID)
	if err != nil || len(chat.Settings.SkillsV2.Bindings) != 0 || chat.Settings.SkillRuntime == nil || chat.Settings.SkillRuntime.Status != "delivered" {
		t.Fatalf("managed workflow receipt/lock: %+v %v", chat, err)
	}
}

func TestManagedOptionalSkillRevocationStopsCachedDelivery(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	agent.Skills.Bindings[0].Optional = true
	s := &managedSkillSession{core: c, settings: ChatSettings{SkillsV2: agent.Skills, SkillsLock: agent.SkillsLock}}
	input := agentic.ModelInput{SystemPrompt: "policy", Message: "task"}
	if _, err := s.prepare(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	defer s.runtime.Close()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	view, err := host.View(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := host.Update(context.Background(), view.Revision, func(tx *skills.SkillHostTransaction) error {
		return tx.Approve(context.Background(), v.Ref, v.Digest, false)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.prepare(context.Background(), input); err == nil || !strings.Contains(err.Error(), "context_resync_required") {
		t.Fatalf("revoked optional body remained deliverable: %v", err)
	}
}

func TestChatDeliversControlledPayloadAndPersistsSanitizedReceipt(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error { return tx.Approve(ctx, version.Ref, version.Digest, true) }); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	mock := &mockAdapter{name: "claude", resumable: true, replies: []string{"one", "two"}}
	withMockAdapter(t, mock)
	if _, err := c.upsertAgent(ctx, agent); err != nil {
		t.Fatal(err)
	}
	chat, err := c.StartInteractiveChat(ctx, agent.ID, "claude", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ContinueChat(ctx, chat.ID, "first", chat.WorkspacePath, "agent rules"); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") || !strings.Contains(mock.shots[0].SystemPrompt, version.Digest) {
		t.Fatalf("first controlled request: %+v", mock.shots)
	}
	if _, err = c.ContinueChat(ctx, chat.ID, "second", chat.WorkspacePath, "agent rules"); err != nil {
		t.Fatal(err)
	}
	if len(mock.resumes) != 1 || !strings.Contains(mock.resumes[0].Message, "EXACT INSTRUCTIONS") || !strings.Contains(mock.resumes[0].Message, version.Digest) {
		t.Fatalf("resume controlled request: %+v", mock.resumes)
	}
	reopened, err := c.GetChat(ctx, chat.ID)
	if err != nil {
		t.Fatal(err)
	}
	state := reopened.Settings.SkillRuntime
	if state == nil || state.Status != "delivered" || state.Coverage != skills.CoverageControlledPayload || len(state.Delivered) == 0 {
		t.Fatalf("receipt state: %+v", state)
	}
	if strings.Contains(fmt.Sprintf("%+v", state), "EXACT INSTRUCTIONS") {
		t.Fatal("receipt persisted skill body")
	}
}

func TestWorkflowUsesSameControlledPayloadPlanAsChat(t *testing.T) {
	c, agent, version := v2AgentFixture(t)
	ctx := context.Background()
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	view, err := host.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error { return tx.Approve(ctx, version.Ref, version.Digest, true) }); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	mock := &mockAdapter{name: "claude", replies: []string{"done"}}
	withMockAdapter(t, mock)
	result := c.RunWorkflow(ctx, RunOptions{Agent: agent, WorkflowName: "run", CLI: "claude", Cwd: t.TempDir()})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") || !strings.Contains(mock.shots[0].SystemPrompt, version.Digest) {
		t.Fatalf("workflow payload: %+v", mock.shots)
	}
}

func TestChatSkillBudgetIncludesAttachmentsBeforeAdapter(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	mock := &mockAdapter{name: "claude", replies: []string{"must not run"}}
	withMockAdapter(t, mock)
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{CLIAgent: "claude", WorkspacePath: t.TempDir(), Settings: ChatSettings{SkillsV2: agent.Skills, SkillsLock: agent.SkillsLock}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ContinueChatWithAttachments(ctx, chat.ID, "review", chat.WorkspacePath, "policy", []string{strings.Repeat("attachment-path", 4000)})
	if err == nil || !strings.Contains(err.Error(), "context_budget_exceeded") || len(mock.shots) != 0 {
		t.Fatalf("oversized final request reached adapter: %v", err)
	}
}

func TestChatNonresumableSkillPayloadIsNotDuplicated(t *testing.T) {
	c, agent, v := v2AgentFixture(t)
	approveRuntimeFixture(t, v)
	mock := &mockAdapter{name: "claude", replies: []string{"first", "second"}}
	withMockAdapter(t, mock)
	ctx := context.Background()
	chat, err := c.CreateChat(ctx, CreateChatRequest{CLIAgent: "claude", WorkspacePath: t.TempDir(), Settings: ChatSettings{SkillsV2: agent.Skills, SkillsLock: agent.SkillsLock}})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first", "second"} {
		if _, err := c.ContinueChat(ctx, chat.ID, message, chat.WorkspacePath, "policy"); err != nil {
			t.Fatal(err)
		}
	}
	if len(mock.shots) != 2 {
		t.Fatalf("shots: %d", len(mock.shots))
	}
	for _, shot := range mock.shots {
		if strings.Count(shot.SystemPrompt+shot.Message, "EXACT INSTRUCTIONS") != 1 {
			t.Fatalf("duplicated payload: %+v", shot)
		}
	}
}

func TestInstalledSkillPickerBuildsExactLockWithoutGrantingTrust(t *testing.T) {
	c, _, version := v2AgentFixture(t)
	ctx := context.Background()
	choices := []InstalledSkillChoice{{Ref: version.Ref, Digest: version.Digest, Activation: "pinned"}}
	if _, err := c.BuildInstalledSkillSelection(ctx, choices); err == nil || !strings.Contains(err.Error(), "untrusted_source") {
		t.Fatalf("picker granted trust: %v", err)
	}
	approveRuntimeFixture(t, version)
	selection, err := c.BuildInstalledSkillSelection(ctx, choices)
	if err != nil {
		t.Fatal(err)
	}
	if len(selection.Lock.Entries) != 1 || selection.Lock.Entries[0].Digest != version.Digest {
		t.Fatalf("picker substituted a digest: %+v", selection)
	}
	chat, err := c.CreateChat(ctx, CreateChatRequest{CLIAgent: "claude", WorkspacePath: t.TempDir(), Settings: ChatSettings{SkillsV2: selection.Config, SkillsLock: selection.Lock}})
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockAdapter{name: "claude", replies: []string{"done"}}
	withMockAdapter(t, mock)
	if _, err := c.ContinueChat(ctx, chat.ID, "review", chat.WorkspacePath, "policy"); err != nil {
		t.Fatal(err)
	}
	if len(mock.shots) != 1 || !strings.Contains(mock.shots[0].SystemPrompt, "EXACT INSTRUCTIONS") {
		t.Fatal("picker selection not delivered")
	}
	if _, err := c.BuildInstalledSkillSelection(ctx, append(choices, choices...)); err == nil {
		t.Fatal("duplicate ref accepted")
	}
	choices[0].Digest = "sha256:wrong"
	if _, err := c.BuildInstalledSkillSelection(ctx, choices); err == nil {
		t.Fatal("foreign digest accepted")
	}
	empty, err := c.BuildInstalledSkillSelection(ctx, []InstalledSkillChoice{})
	if err != nil || !empty.Config.Configured || len(empty.Config.Bindings) != 0 {
		t.Fatalf("explicit empty: %+v %v", empty, err)
	}
}
