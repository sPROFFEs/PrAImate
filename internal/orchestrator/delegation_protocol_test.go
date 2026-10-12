package orchestrator

import (
	"context"
	"strings"
	"testing"

	workerruntime "github.com/sPROFFEs/PrAImate/internal/runtime"
)

func TestBuiltInDelegationCorrectsRawPlanWithoutInstalledSkill(t *testing.T) {
	primary := &fakeWorker{responses: []string{`{"tasks":[{"id":"task-a","description":"Inspect the project","worker":{"profile":"middle"}}]}`, `{"action":"delegate","tier":"middle","task":"Inspect the project and report findings"}`, `{"action":"final","content":"Verified child findings"}`}}
	middle := &fakeWorker{responses: []string{`{"action":"final","content":"inspected the project"}`}}
	runner := Runner{Resolve: func(p Profile) (workerruntime.Runtime, error) {
		if p.Tier == Primary {
			return primary, nil
		}
		return middle, nil
	}}
	result, err := runner.Run(context.Background(), testConfig(t), "Inspect the project using the available workers")
	if err != nil || result != "Verified child findings" || len(middle.requests) != 1 || len(primary.requests) != 3 {
		t.Fatalf("delegation without skills failed: %q %v", result, err)
	}
	if !strings.Contains(primary.requests[1].Task, "action=delegate") || !strings.Contains(primary.requests[0].SystemPrompt, "No external orchestration skill") {
		t.Fatal("reasoner was not given explicit built-in delegation guidance")
	}
}
