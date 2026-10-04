package orchestrator

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestRunProfileUpdatesPersistWithoutRelabellingActivity(t *testing.T) {
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	id := "worker-settings"
	ctx := context.Background()
	if _, err = c.CreateChat(ctx, core.CreateChatRequest{ID: id, CLIAgent: "workers", Title: "test", WorkspacePath: config.Workspace, Settings: core.ChatSettings{Surface: "workers"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(config)
	if _, err = c.AddMessage(ctx, id, "system", string(raw), nil); err != nil {
		t.Fatal(err)
	}
	m := NewManager(c, ctx)
	m.runs[id] = &Run{ID: id, Workspace: config.Workspace, Profiles: config.Profiles, config: config, Status: "failed", Events: []Event{{Tier: Primary, WorkerID: "past", Model: "model-primary"}}}
	updated := config
	updated.Profiles = append([]Profile(nil), config.Profiles...)
	updated.Profiles[0].Model = "new-model"
	updated.Profiles[0].TimeoutSeconds = 0
	if err = m.UpdateRunConfig(id, updated); err != nil {
		t.Fatal(err)
	}
	updated.Profiles[0].Model = "caller-mutated-model"
	if m.runs[id].config.Profiles[0].Model != "new-model" {
		t.Fatal("caller changed the saved runtime route without an update")
	}
	restored := NewManager(c, ctx)
	snapshot, err := restored.Snapshot(id)
	if err != nil || snapshot.Profiles[0].Model != "new-model" || snapshot.Events[0].Model != "model-primary" || restored.runs[id].config.Profiles[0].TimeoutSeconds != 0 {
		t.Fatalf("updated configuration/history lost: %+v %v", snapshot, err)
	}
	bad := updated
	bad.Workspace = t.TempDir()
	if m.UpdateRunConfig(id, bad) == nil {
		t.Fatal("changed existing project identity")
	}
	m.runs[id].cancel = func() {}
	if m.UpdateRunConfig(id, updated) == nil {
		t.Fatal("updated active process")
	}
	if restored.RetryPlanning(id) == nil {
		t.Fatal("accepted planning retry for hierarchical run")
	}
	restored.runs[id].DAG = &DAG{Tasks: []DAGTask{{ID: "existing"}}}
	if restored.RetryPlanning(id) == nil {
		t.Fatal("retried planning over existing tasks")
	}
}

type retryPlanAdapter struct {
	dagAdapter
	models []string
}

func (a *retryPlanAdapter) SingleShot(_ context.Context, opts core.SingleShotOpts) (*core.Reply, error) {
	a.models = append(a.models, opts.Model)
	if len(a.models) == 1 {
		return nil, context.DeadlineExceeded
	}
	return &core.Reply{Text: `{"tasks":[{"id":"retry-task","description":"Inspect one file","dependencies":[],"worker":{"profile":"fast"}}]}`}, nil
}

func TestFailedPlanningRetriesOnlyWhenRequestedAndUsesSavedProfiles(t *testing.T) {
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "state.db"), "test-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	config := testConfig(t)
	config.Workspace = gitFixture(t)
	for i := range config.Profiles {
		config.Profiles[i].Runtime = "cli"
		config.Profiles[i].CLI = "codex"
		config.Profiles[i].Endpoint = ""
		config.Profiles[i].MaxOutputTokens = 0
	}
	adapter := &retryPlanAdapter{dagAdapter: dagAdapter{name: "codex"}}
	previous, _ := core.GetCLIAdapter("codex")
	core.RegisterCLIAdapter(adapter)
	t.Cleanup(func() {
		if previous != nil {
			core.RegisterCLIAdapter(previous)
		} else {
			core.UnregisterCLIAdapter("codex")
		}
	})
	m := NewManager(c, context.Background())
	id, err := m.PlanDAG("Inspect the project", config, 1)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitGraph(t, m, id, "failed")
	if len(adapter.models) != 1 || failed.DAG.Tasks != nil {
		t.Fatal("planning was automatically retried")
	}
	config.Profiles[0].Model = "new-reasoner"
	config.Profiles[0].TimeoutSeconds = 0
	if err = m.UpdateRunConfig(id, config); err != nil {
		t.Fatal(err)
	}
	if err = m.RetryPlanning(id); err != nil {
		t.Fatal(err)
	}
	draft := waitGraph(t, m, id, "draft")
	if len(draft.DAG.Tasks) != 1 || len(adapter.models) != 2 || adapter.models[1] != "new-reasoner" || draft.Error != "" {
		t.Fatalf("retry lost updated route: %+v %v", draft, adapter.models)
	}
	if err = m.RetryPlanning(id); err == nil {
		t.Fatal("existing tasks were replaced by planning retry")
	}
}
