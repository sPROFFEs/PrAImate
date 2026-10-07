package agentic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type scriptedModel struct {
	outputs []string
	inputs  []ModelInput
	err     error
}

type blockingExternalTool struct{ started chan struct{} }

func (t *blockingExternalTool) Instructions() string {
	return `{"action":"tool","tool":"external.block","arguments":{}}`
}
func (t *blockingExternalTool) ExecuteTool(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
	close(t.started)
	<-ctx.Done()
	return "", ctx.Err()
}

func (m *scriptedModel) Turn(_ context.Context, input ModelInput, _ EventSink) (*ModelOutput, error) {
	m.inputs = append(m.inputs, input)
	if m.err != nil {
		return nil, m.err
	}
	if len(m.outputs) == 0 {
		return nil, errors.New("script exhausted")
	}
	out := m.outputs[0]
	m.outputs = m.outputs[1:]
	return &ModelOutput{Text: out, SessionID: "session-1"}, nil
}

func TestPrepareArtifactsFailureRetainsCheckpoint(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			root := t.TempDir()
			model := &scriptedModel{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			prepareErr := errors.New("source artifact is unavailable")
			wantState := StateFailed
			result, err := Run(ctx, Config{
				RootDir: root, RunID: "next-step", AgentID: "reviewer", Task: "Continue the workflow.", Model: model,
				PrepareArtifacts: func(_ context.Context, accept func(ArtifactInput) error) error {
					if err := accept(ArtifactInput{Name: "report.md", Content: "Existing work", SourceRunID: "first-step"}); err != nil {
						return err
					}
					if cancelled {
						cancel()
						prepareErr, wantState = context.Canceled, StateStopped
					}
					return prepareErr
				},
			})
			if !errors.Is(err, prepareErr) || result == nil || result.Instance.State != wantState || len(model.inputs) != 0 {
				t.Fatalf("result=%+v err=%v model calls=%d", result, err, len(model.inputs))
			}
			loaded, _, _, err := loadRunState(filepath.Join(root, "next-step"))
			if err != nil || loaded.State != wantState || loaded.Error != prepareErr.Error() || loaded.InputLimitBytes == 0 || len(loaded.Artifacts) != 1 {
				t.Fatalf("checkpoint=%+v err=%v", loaded, err)
			}
			body, err := ReadArtifact(root, "next-step", "report.md")
			if err != nil || string(body) != "Existing work" {
				t.Fatalf("artifact=%q err=%v", body, err)
			}
		})
	}
}

func TestRunLifecycleMemoryArtifactsAndExplicitFinish(t *testing.T) {
	root := t.TempDir()
	model := &scriptedModel{outputs: []string{
		`{"action":"tool","tool":"memory.task","arguments":{"title":"Inspect","content":"Review the target","status":"active"}}`,
		`{"action":"tool","tool":"memory.fact","arguments":{"content":"The parser is strict"}}`,
		`{"action":"tool","tool":"artifact.write","arguments":{"name":"report.md","content":"# Report\nComplete"}}`,
		`{"action":"finish","message":"Review completed."}`,
	}}
	var events []Event
	result, err := Run(context.Background(), Config{
		RootDir: root, AgentID: "reviewer", AgentName: "Reviewer",
		Instructions: "Review carefully.", Task: "Inspect the project.", Model: model,
		OnEvent: func(event Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Instance.State != StateCompleted || result.Instance.Turns != 4 || result.Instance.Final != "Review completed." {
		t.Fatalf("instance = %#v", result.Instance)
	}
	if len(result.Memory.Items) != 2 || len(result.Instance.Artifacts) != 1 {
		t.Fatalf("memory=%#v artifacts=%#v", result.Memory, result.Instance.Artifacts)
	}
	artifactPath := filepath.Join(root, result.Instance.ID, "artifacts", "report.md")
	if body, err := os.ReadFile(artifactPath); err != nil || !strings.Contains(string(body), "Complete") {
		t.Fatalf("artifact body=%q err=%v", body, err)
	}
	for _, forbidden := range []string{"events.jsonl", "runtime.log", "agent.log"} {
		if _, err := os.Stat(filepath.Join(root, result.Instance.ID, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("runtime created forbidden log file %s", forbidden)
		}
	}
	if len(events) == 0 || events[0].Type != "run.started" || events[len(events)-1].Type != "run.finished" {
		t.Fatalf("events = %#v", events)
	}
	loaded, err := LoadInstance(root, result.Instance.ID)
	if err != nil || loaded.State != StateCompleted {
		t.Fatalf("loaded=%#v err=%v", loaded, err)
	}
}

func TestRunBoundsFinalOutputIntoArtifact(t *testing.T) {
	long := strings.Repeat("x", 1200)
	model := &scriptedModel{outputs: []string{`{"action":"finish","message":"` + long + `"}`}}
	root := t.TempDir()
	result, err := Run(context.Background(), Config{
		RootDir: root, AgentID: "bounded", AgentName: "Bounded",
		Task: "Answer.", Model: model,
		Limits: Limits{MaxTurns: 2, MaxContextChars: 2000, MaxOutputChars: 500, MaxArtifactSize: 4096},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Instance.Final) >= len(long) || !strings.Contains(result.Instance.Final, "artifact://final-output.txt") {
		t.Fatalf("final was not bounded: %d %q", len(result.Instance.Final), result.Instance.Final)
	}
	if len(result.Instance.Artifacts) != 1 || result.Instance.Artifacts[0].Size != int64(len(long)) {
		t.Fatalf("artifacts = %#v", result.Instance.Artifacts)
	}
	body, err := ReadArtifact(root, result.Instance.ID, "final-output.txt")
	if err != nil || string(body) != long {
		t.Fatalf("full output artifact size=%d err=%v", len(body), err)
	}
}

func TestRunRejectsRepeatedPlainTextCompletion(t *testing.T) {
	model := &scriptedModel{outputs: []string{"I am done", "still done", "finished"}}
	result, err := Run(context.Background(), Config{
		RootDir: t.TempDir(), AgentID: "strict", AgentName: "Strict",
		Task: "Answer.", Model: model, Limits: Limits{MaxTurns: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "violated runtime protocol") {
		t.Fatalf("err = %v", err)
	}
	if result == nil || result.Instance.State != StateFailed || result.Instance.Turns != 3 {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunStopsAtTurnLimitWithoutFinish(t *testing.T) {
	model := &scriptedModel{outputs: []string{
		`{"action":"continue","message":"one"}`,
		`{"action":"continue","message":"two"}`,
	}}
	result, err := Run(context.Background(), Config{
		RootDir: t.TempDir(), AgentID: "stalled", AgentName: "Stalled",
		Task: "Continue.", Model: model, Limits: Limits{MaxTurns: 2},
	})
	if err == nil || result.Instance.State != StateStalled {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestReadArtifactRejectsPathTraversal(t *testing.T) {
	if _, err := ReadArtifact(t.TempDir(), "../run", "secret"); err == nil {
		t.Fatal("run traversal accepted")
	}
	if _, err := ReadArtifact(t.TempDir(), "run", "../secret"); err == nil {
		t.Fatal("artifact traversal accepted")
	}
}

func TestMemorySummaryKeepsNewestItemsWhenBounded(t *testing.T) {
	memory := Memory{Items: []MemoryItem{
		{Kind: "fact", Content: strings.Repeat("old", 40)},
		{Kind: "decision", Content: "keep the newest decision"},
	}}
	summary := memory.summary(90)
	if strings.Contains(summary, "oldold") || !strings.Contains(summary, "keep the newest decision") {
		t.Fatalf("summary = %q", summary)
	}
	if !strings.Contains(summary, "older working-memory items omitted") {
		t.Fatalf("missing omission marker: %q", summary)
	}
}

func TestRunResumesDurableCheckpoint(t *testing.T) {
	root := t.TempDir()
	first := &scriptedModel{outputs: []string{`{"action":"tool","tool":"memory.fact","arguments":{"content":"checkpoint fact"}}`}}
	failed, err := Run(context.Background(), Config{
		RootDir: root, AgentID: "resume", AgentName: "Resume", Task: "Continue safely.", Model: first, Limits: Limits{MaxTurns: 4},
	})
	if err == nil || failed.Instance.State != StateFailed {
		t.Fatalf("first result=%#v err=%v", failed, err)
	}
	second := &scriptedModel{outputs: []string{`{"action":"finish","message":"Recovered."}`}}
	resumed, err := Run(context.Background(), Config{
		RootDir: root, AgentID: "resume", AgentName: "Resume", Task: "Continue safely.", Model: second,
		ResumeRunID: failed.Instance.ID, Limits: Limits{MaxTurns: 4},
	})
	if err != nil || resumed.Instance.State != StateCompleted || resumed.Instance.Turns != 3 || resumed.Instance.ID != failed.Instance.ID {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	if len(second.inputs) != 1 || !strings.Contains(second.inputs[0].Message, "checkpoint fact") {
		t.Fatalf("resume prompt = %#v", second.inputs)
	}
}

func TestInterruptedToolIsMarkedUnknownBeforeResume(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	tool := &blockingExternalTool{started: make(chan struct{})}
	type runResult struct {
		result *Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, err := Run(ctx, Config{
			RootDir: root, AgentID: "interrupted", AgentName: "Interrupted", Task: "Use the tool.",
			Model: &scriptedModel{outputs: []string{`{"action":"tool","tool":"external.block","arguments":{}}`}}, Tools: tool,
		})
		done <- runResult{result: result, err: err}
	}()
	<-tool.started
	cancel()
	stopped := <-done
	if stopped.err == nil || stopped.result.Instance.State != StateStopped || stopped.result.Instance.PendingTool != "external.block" {
		t.Fatalf("stopped=%#v err=%v", stopped.result, stopped.err)
	}
	model := &scriptedModel{outputs: []string{`{"action":"finish","message":"Recovered after inspection."}`}}
	resumed, err := Run(context.Background(), Config{
		RootDir: root, AgentID: "interrupted", AgentName: "Interrupted", Task: "Use the tool.",
		Model: model, Tools: tool, ResumeRunID: stopped.result.Instance.ID,
	})
	if err != nil || resumed.Instance.State != StateCompleted || !strings.Contains(model.inputs[0].Message, "outcome is unknown") {
		t.Fatalf("resumed=%#v prompt=%#v err=%v", resumed, model.inputs, err)
	}
}

func TestParseDecisionRobustPreambleThinkingAndMultipleJSON(t *testing.T) {
	cases := []struct {
		input  string
		action string
		tool   string
	}{
		{
			input:  `<think>Let me see what needs to be done</think>{"action":"tool","tool":"memory.task","arguments":{"content":"check"}}`,
			action: "tool",
			tool:   "memory.task",
		},
		{
			input:  "I have thought about it.\n```json\n{\"action\":\"finish\",\"message\":\"Done!\"}\n```",
			action: "finish",
		},
		{
			input:  "Some thought {in braces} and then:\n{\"action\":\"continue\",\"message\":\"thinking more\"}",
			action: "continue",
		},
		{
			input:  "Here is my result: {\"final_answer\": \"Everything passed.\"}",
			action: "finish",
		},
	}

	for _, c := range cases {
		dec, err := parseDecision(c.input)
		if err != nil {
			t.Fatalf("parseDecision(%q) failed: %v", c.input, err)
		}
		if dec.Action != c.action {
			t.Fatalf("parseDecision(%q).Action = %q, want %q", c.input, dec.Action, c.action)
		}
		if c.tool != "" && dec.Tool != c.tool {
			t.Fatalf("parseDecision(%q).Tool = %q, want %q", c.input, dec.Tool, c.tool)
		}
	}
}

func TestReadArtifactRejectsSymlinksAcrossWorkflowSteps(t *testing.T) {
	root := t.TempDir()
	run := filepath.Join(root, "run", "artifacts")
	if err := os.MkdirAll(run, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(target, []byte("fixture data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(run, "report.md")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := ReadArtifact(root, "run", "report.md"); err == nil {
		t.Fatal("inherited artifact followed a symlink")
	}
}
