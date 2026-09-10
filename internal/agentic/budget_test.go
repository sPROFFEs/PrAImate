package agentic

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type preparedBudgetModel struct{ scriptedModel }

func (m *preparedBudgetModel) PrepareInput(_ context.Context, in ModelInput) (ModelInput, error) {
	in.SystemPrompt += strings.Repeat("s", 4000)
	in.SkillBytes = 4000
	return in, nil
}

func TestCumulativeInputBudgetIncludesPreparedSkillsAndFailedResumes(t *testing.T) {
	root := t.TempDir()
	model := &preparedBudgetModel{scriptedModel: scriptedModel{err: errors.New("transport interrupted")}}
	cfg := Config{RootDir: root, AgentID: "budget", Task: "Test", Model: model, Limits: Limits{MaxTotalInputBytes: 8000}}
	first, err := Run(context.Background(), cfg)
	if err == nil || first == nil || first.Instance.SkillBytes != 4000 || first.Instance.InputBytes <= 4000 {
		t.Fatal("failed call was not charged including skill payload", first, err)
	}
	loaded, err := LoadInstance(root, first.Instance.ID)
	if err != nil || loaded.InputBytes != first.Instance.InputBytes {
		t.Fatal("input reservation was not persisted", err)
	}
	second := &preparedBudgetModel{scriptedModel: scriptedModel{outputs: []string{`{"action":"finish","message":"cannot run"}`}}}
	cfg.ResumeRunID, cfg.Model = first.Instance.ID, second
	cfg.Limits.MaxTotalInputBytes = 1 << 20 // A resumed attempt cannot widen it.
	result, err := Run(context.Background(), cfg)
	if err == nil || len(second.inputs) != 0 || result.Instance.InputLimitBytes != 8000 || result.Instance.InputBytes != first.Instance.InputBytes {
		t.Fatal("resume reset or widened cumulative input budget", result, err)
	}
}
