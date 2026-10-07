package core

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/agentic"
)

type partialWorkflowAdapter struct {
	mockAdapter
	cancel       context.CancelFunc
	noFinalReply bool
}

func (a *partialWorkflowAdapter) SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	emit(StreamEvent{Type: "status", ID: "retained-session", Detail: "CLI session initialized."})
	emit(StreamEvent{Type: "text", Text: "partial result"})
	a.cancel()
	if a.noFinalReply {
		return nil, ctx.Err()
	}
	return &Reply{SessionID: "retained-session", Text: "partial result"}, ctx.Err()
}

func TestCancelledWorkflowRetainsPartialReplyAndSession(t *testing.T) {
	for _, noFinalReply := range []bool{false, true} {
		t.Run(fmt.Sprint(noFinalReply), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			adapter := &partialWorkflowAdapter{mockAdapter: mockAdapter{name: "partial-workflow", resumable: true}, cancel: cancel, noFinalReply: noFinalReply}
			RegisterCLIAdapter(adapter)
			defer UnregisterCLIAdapter(adapter.Name())
			c, _ := New(Options{Store: openTempStore(t)})
			agent := &Agent{ID: "partial", Name: "Partial", Supports: []string{adapter.Name()}, Workflows: []Workflow{{Name: "go", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "work"}}}}}
			var delivered []TurnResult
			res := c.RunWorkflow(ctx, RunOptions{Agent: agent, WorkflowName: "go", CLI: adapter.Name(), Cwd: t.TempDir(), Persist: true, OnTurn: func(turn TurnResult) { delivered = append(delivered, turn) }})
			if res.Outcome != OutcomeCancelled || res.SessionID != "retained-session" || len(res.Turns) != 1 || len(delivered) != 1 {
				t.Fatalf("partial result lost: %+v", res)
			}
			chat, err := c.GetChat(context.Background(), res.ChatID)
			if err != nil || chat.SessionID != res.SessionID || chat.ExitKind != "cancelled" {
				t.Fatalf("chat not recoverable: %+v %v", chat, err)
			}
			messages, err := c.ListMessages(context.Background(), res.ChatID, 0)
			if err != nil || len(messages) != 2 || messages[1].Content != "partial result" {
				t.Fatalf("transcript lost: %+v %v", messages, err)
			}
		})
	}
}

func TestWorkflowUntilToolRequiresSuccessfulCompletionBeforeNextTask(t *testing.T) {
	for _, ok := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "completed"}[ok], func(t *testing.T) {
			adapter := &mockAdapter{name: "gated-workflow", resumable: true, streamEvents: []StreamEvent{{Type: "tool_start", ID: "tool-1", Tool: "shell"}, {Type: "tool_end", ID: "tool-1", OK: ok}}}
			withMockAdapter(t, adapter)
			c, _ := New(Options{Store: openTempStore(t)})
			agent := &Agent{ID: "gate", Name: "Gate", Supports: []string{adapter.Name()}, Workflows: []Workflow{{Name: "go", Steps: []WorkflowStep{{Kind: StepUserMessage, Template: "run a command"}, {Kind: StepWaitForAssistant, UntilTool: "shell"}, {Kind: StepUserMessage, Template: "next task"}}}}}
			res := c.RunWorkflow(context.Background(), RunOptions{Agent: agent, WorkflowName: "go", CLI: adapter.Name(), Cwd: t.TempDir(), Persist: true})
			if ok && (res.Err != nil || len(res.Turns) != 2) {
				t.Fatalf("successful gate blocked: %+v", res)
			}
			if !ok && (res.Err == nil || len(res.Turns) != 1 || len(adapter.resumes) != 0) {
				t.Fatalf("later task ran before its gate: %+v", res)
			}
			chat, err := c.GetChat(context.Background(), res.ChatID)
			if err != nil || chat.SessionID == "" {
				t.Fatalf("prior session lost: %+v %v", chat, err)
			}
		})
	}
}

func TestManagedWorkflowRunsStepsSeparatelyAndChecksToolGate(t *testing.T) {
	for _, gate := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing tool", true: "completed tool"}[gate], func(t *testing.T) {
			withTempConfigDir(t)
			replies := []string{`{"action":"finish","message":"first result"}`}
			if gate {
				replies = append([]string{`{"action":"tool","tool":"memory.task","arguments":{"title":"step","content":"checked"}}`}, replies...)
			}
			replies = append(replies, `{"action":"finish","message":"second result"}`)
			mock := &mockAdapter{name: "managed-step-workflow", replies: replies}
			withMockAdapter(t, mock)
			c, _ := New(Options{Store: openTempStore(t)})
			agent := autonomousTestAgent("step-agent", mock.name)
			saveAutonomousRuntime(t, agent.ID)
			agent.Workflows[0].Steps = []WorkflowStep{{Kind: StepUserMessage, Template: "FIRST_STEP"}, {Kind: StepWaitForAssistant, UntilTool: "memory.task"}, {Kind: StepUserMessage, Template: "SECOND_STEP"}}
			result := c.RunWorkflow(context.Background(), RunOptions{Agent: agent, WorkflowName: "review", Inputs: map[string]string{"target": "src"}, CLI: mock.name, Cwd: t.TempDir(), Persist: true})
			if gate {
				if result.Err != nil || len(result.Turns) != 2 || len(mock.shots) != 3 {
					t.Fatalf("steps failed: %+v %v", result, result.Err)
				}
				if !strings.Contains(mock.shots[2].Message, "SECOND_STEP") || !strings.Contains(mock.shots[2].Message, "first result") {
					t.Fatal("next step lost its handoff")
				}
			} else if result.Err == nil || len(result.Turns) != 1 || len(mock.shots) != 1 {
				t.Fatalf("later step bypassed tool gate: %+v", result)
			}
			if strings.Contains(mock.shots[0].Message, "SECOND_STEP") {
				t.Fatal("workflow flattened into one request")
			}
		})
	}
}

func TestManagedWorkflowReportsCLIReasoningAndCorrelatesToolCompletion(t *testing.T) {
	withTempConfigDir(t)
	mock := &mockAdapter{name: "reported-workflow", replies: []string{`{"action":"finish","message":"checked"}`, `{"action":"finish","message":"continued"}`}, streamEvents: []StreamEvent{
		{Type: "reasoning", Text: "Inspecting the requested file."},
		{Type: "tool_start", Tool: "read", ID: "read-1"},
		{Type: "tool_end", ID: "read-1", OK: true},
	}}
	withMockAdapter(t, mock)
	c, _ := New(Options{Store: openTempStore(t)})
	agent := autonomousTestAgent("reported-agent", mock.name)
	saveAutonomousRuntime(t, agent.ID)
	agent.Workflows[0].Steps = []WorkflowStep{{Kind: StepUserMessage, Template: "inspect"}, {Kind: StepWaitForAssistant, UntilTool: "read"}, {Kind: StepUserMessage, Template: "continue"}}
	var reported []WorkflowRunEvent
	result := c.RunWorkflow(context.Background(), RunOptions{Agent: agent, WorkflowName: "review", Inputs: map[string]string{"target": "src"}, CLI: mock.name, Cwd: t.TempDir(), OnEvent: func(ev WorkflowRunEvent) { reported = append(reported, ev) }})
	if result.Err != nil || len(result.Turns) != 2 {
		t.Fatalf("observable tool gate failed: %+v %v", result, result.Err)
	}
	found := false
	for _, ev := range reported {
		if ev.Type == "reasoning" && ev.Text == "Inspecting the requested file." {
			found = true
		}
	}
	if !found {
		t.Fatal("reported CLI progress disappeared in the managed workflow bridge")
	}
}

func TestWorkflowCompletionMarkerRemainsCompatible(t *testing.T) {
	for _, managed := range []bool{false, true} {
		t.Run(fmt.Sprint(managed), func(t *testing.T) {
			withTempConfigDir(t)
			replies := []string{"first", "second"}
			if managed {
				replies = []string{`{"action":"finish","message":"first"}`, `{"action":"finish","message":"second"}`}
			}
			mock := &mockAdapter{name: "completion-marker", resumable: true, replies: replies}
			withMockAdapter(t, mock)
			c, _ := New(Options{Store: openTempStore(t)})
			agent := autonomousTestAgent("completion-marker-agent", mock.name)
			if managed {
				saveAutonomousRuntime(t, agent.ID)
			}
			agent.Workflows[0].Steps = []WorkflowStep{{Kind: StepUserMessage, Template: "first"}, {Kind: StepWaitForAssistant, UntilTool: "complete"}, {Kind: StepUserMessage, Template: "second"}}
			result := c.RunWorkflow(context.Background(), RunOptions{Agent: agent, WorkflowName: "review", Inputs: map[string]string{"target": "src"}, CLI: mock.name, Cwd: t.TempDir()})
			if result.Err != nil || len(result.Turns) != 2 {
				t.Fatalf("legacy marker broke: %+v %v", result, result.Err)
			}
		})
	}
}

func TestManagedWorkflowCarriesArtifactsAndVerifiesFinalBytes(t *testing.T) {
	for _, valid := range []bool{false, true} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			withTempConfigDir(t)
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte("expected report")))
			if !valid {
				hash = fmt.Sprintf("%x", sha256.Sum256([]byte("different report")))
			}
			replies := []string{`{"action":"tool","tool":"artifact.write","arguments":{"name":"report.md","content":"expected report"}}`, `{"action":"finish","message":"report produced"}`}
			for i := 0; i < 8; i++ {
				replies = append(replies, `{"action":"finish","message":"report checked"}`)
			}
			mock := &mockAdapter{name: "artifact-workflow", replies: replies}
			withMockAdapter(t, mock)
			c, _ := New(Options{Store: openTempStore(t)})
			agent := autonomousTestAgent("artifact-step-agent", mock.name)
			saveAutonomousRuntime(t, agent.ID)
			agent.Workflows[0].Steps = []WorkflowStep{{Kind: StepUserMessage, Template: "Produce report.md"}, {Kind: StepUserMessage, Template: "Verify the report and finish"}}
			agent.Workflows[0].FinishEvidence = []agentic.EvidenceRequirement{{Artifact: "report.md", MinBytes: 10, SHA256: hash}}
			result := c.RunWorkflow(context.Background(), RunOptions{Agent: agent, WorkflowName: "review", Inputs: map[string]string{"target": "src"}, CLI: mock.name, Cwd: t.TempDir(), Persist: true})
			if valid && (result.Err != nil || len(result.Turns) != 2) {
				t.Fatalf("artifact handoff lost: %+v %v", result, result.Err)
			}
			if !valid && (result.Err == nil || result.Outcome == OutcomeCompleted) {
				t.Fatal("inherited artifact bypassed its hash check")
			}
			run, err := c.GetManagedRun(result.RunID)
			if err != nil || run.EvidenceVerified != valid {
				t.Fatalf("wrong evidence state: %+v %v", run, err)
			}
			source := ""
			for _, artifact := range run.Artifacts {
				if artifact.Name == "report.md" {
					source = artifact.SourceRunID
				}
			}
			if source == "" || source == run.ID {
				t.Fatal("artifact lost its originating step")
			}
			body, err := c.ReadManagedArtifact(run.ID, "report.md")
			if err != nil || string(body) != "expected report" {
				t.Fatalf("artifact bytes changed: %q %v", body, err)
			}
		})
	}
}
