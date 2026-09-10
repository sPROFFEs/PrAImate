package core

import (
	"context"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/agentic"
)

func TestWorkflowEvidenceRoundTripAndNativeRefusal(t *testing.T) {
	c, a, _ := v2AgentFixture(t)
	a.Skills, a.SkillsLock = nil, nil
	a.Workflows[0].FinishEvidence = []agentic.EvidenceRequirement{{Artifact: "report.md", MinBytes: 10}}
	body, err := MarshalAgentYAML(a)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAgentYAML(strings.NewReader(string(body)))
	if err != nil || len(parsed.Workflows[0].FinishEvidence) != 1 || parsed.Workflows[0].FinishEvidence[0].MinBytes != 10 {
		t.Fatal("workflow evidence lost in YAML", err)
	}
	result := c.RunWorkflow(context.Background(), RunOptions{Agent: parsed, WorkflowName: "run", CLI: "claude", Cwd: t.TempDir()})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "requires managed artifact verification") {
		t.Fatal("native workflow accepted unverifiable finish", result.Err)
	}
}
