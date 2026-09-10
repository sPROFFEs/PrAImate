package agentic

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
)

func TestFinishRequiresHostVerifiedArtifactAndSurvivesResume(t *testing.T) {
	root := t.TempDir()
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte("expected report")))
	first := &scriptedModel{outputs: []string{`{"action":"finish","message":"I verified everything"}`}}
	cfg := Config{RootDir: root, AgentID: "evidence", Task: "Produce report", Model: first, Limits: Limits{MaxTurns: 1}, FinishEvidence: []EvidenceRequirement{{Artifact: "report.md", SHA256: hash}}}
	result, err := Run(context.Background(), cfg)
	if err == nil || result.Instance.State == StateCompleted || result.Instance.EvidenceVerified {
		t.Fatal("self-claim passed finish gate")
	}
	second := &scriptedModel{outputs: []string{
		`{"action":"tool","tool":"artifact.write","arguments":{"name":"report.md","content":"wrong report"}}`,
		`{"action":"finish","message":"Still done"}`,
		`{"action":"tool","tool":"artifact.write","arguments":{"name":"report.md","content":"expected report"}}`,
		`{"action":"finish","message":"Required artifact supplied"}`,
	}}
	cfg.ResumeRunID, cfg.Model, cfg.Limits.MaxTurns = result.Instance.ID, second, 4
	cfg.FinishEvidence = nil // Stored task requirements cannot be removed by resume.
	result, err = Run(context.Background(), cfg)
	if err != nil || result.Instance.State != StateCompleted || !result.Instance.EvidenceVerified || len(second.inputs) != 4 {
		t.Fatal("evidence gate failed or was bypassed", result, err)
	}
}
