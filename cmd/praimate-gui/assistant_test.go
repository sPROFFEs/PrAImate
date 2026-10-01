package main

import (
	"context"
	"encoding/json"
	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAssistantVoiceCaptureLeaseIsolation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Native microphone permission requires a desktop session")
	}
	app := assistantAppFixture(t)
	if _, err := app.BeginVoiceCapture(); err == nil {
		t.Fatal("disabled voice accepted capture")
	}
	config := assistant.DefaultConfig()
	config.Voice.Enabled = true
	raw, _ := json.Marshal(config)
	if err := app.core.SetSetting(context.Background(), core.ScopeGUI, assistantConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	first, err := app.BeginVoiceCapture()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.CancelVoice)
	if _, err := app.BeginVoiceCapture(); err == nil {
		t.Fatal("overlapping capture accepted")
	}
	app.EndVoiceCapture(first.ID)
	second, err := app.BeginVoiceCapture()
	if err != nil {
		t.Fatal(err)
	}
	app.EndVoiceCapture(first.ID)
	if app.voiceCaptureTimer == nil {
		t.Fatal("stale end cancelled current capture")
	}
	app.EndVoiceCapture(second.ID)
	app.assistantClosed = true
	if _, err := app.BeginVoiceCapture(); err == nil {
		t.Fatal("capture started after shutdown")
	}
}

func assistantAppFixture(t *testing.T) *App {
	t.Helper()
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "assistant-test-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return &App{core: c, st: st}
}
func TestAssistantPersistsTypedTasksAndSessionEncrypted(t *testing.T) {
	app := assistantAppFixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			w.Write([]byte(`{"data":[{"id":"fixture"}]}`))
			return
		}
		if r.URL.Path != "/v1/chat/completions" {
			w.WriteHeader(404)
			return
		}
		calls++
		content := `{"type":"action","action":"tasks.create","arguments":{"goal":"private-task-marker-123","tier":"fast"}}`
		if calls > 1 {
			content = `{"type":"final","message":"Task prepared without starting a worker."}`
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 10}})
	}))
	defer server.Close()
	config := assistant.DefaultConfig()
	config.Enabled = true
	config.ModelID = "existing"
	config.Endpoint = server.URL
	config.Model = "fixture"
	config.Permissions["tasks"] = assistant.Allow
	raw, _ := json.Marshal(config)
	if err := app.core.SetSetting(context.Background(), core.ScopeGUI, assistantConfigKey, raw); err != nil {
		t.Fatal(err)
	}
	state, err := app.SendAssistant("Save my private task", `{"page":"dashboard"}`)
	if err != nil {
		t.Fatal(err)
	}
	if state.Task.Status != "completed" || len(state.Activity) != 1 || state.Activity[0].Action != "tasks.create" {
		t.Fatalf("unexpected session %+v", state)
	}
	tasks, err := app.loadApplicationTasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Status != "prepared" {
		t.Fatalf("tasks %v err %v", tasks, err)
	}
	snapshot, err := app.AssistantSnapshot()
	if err != nil || len(snapshot.Messages) != 2 {
		t.Fatalf("snapshot %+v err %v", snapshot, err)
	}
	root := os.Getenv("PRAIMATE_HOME")
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "db.sqlite") {
			bytes, err := os.ReadFile(filepath.Join(root, file.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(bytes), "private-task-marker-123") {
				t.Fatal("task leaked into plaintext database files")
			}
		}
	}
}
func TestAssistantRecoversInterruptedCheckpointWithoutReplay(t *testing.T) {
	app := assistantAppFixture(t)
	state := assistant.State{Messages: []assistant.Message{}, Task: &assistant.Task{Status: "running", Steps: []assistant.Step{{Action: "system.command", Status: "executing"}}}}
	raw, _ := json.Marshal(state)
	if err := app.core.SetSetting(context.Background(), core.ScopeGUI, assistantStateKey, raw); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.AssistantSnapshot()
	if err != nil || snapshot.Task.Status != "interrupted" || snapshot.Task.Steps[0].Status != "executing" {
		t.Fatalf("snapshot %+v err %v", snapshot, err)
	}
}
func TestAssistantActionsHaveKnownCapabilities(t *testing.T) {
	registry := (&App{}).assistantActions()
	known := map[string]bool{}
	for _, cap := range assistant.Capabilities {
		known[cap] = true
	}
	for _, action := range registry.Search("", 6) {
		if !known[action.Capability] {
			t.Fatal("unknown capability")
		}
	}
	for _, name := range []string{"skills.set_agent", "skills.install_url", "mcp.test", "tasks.run", "agents.clone", "delegate.workers", "chats.clone"} {
		action, ok := registry.Get(name)
		if !ok || !known[action.Capability] {
			t.Fatalf("missing typed action %s", name)
		}
	}
}
func TestAssistantReadOnlyCloneRemovesMutatingCapabilities(t *testing.T) {
	app := assistantAppFixture(t)
	ctx := context.Background()
	source := &core.Agent{ID: "source", Name: "Source", Description: "Source agent", Instructions: "Review code", Supports: []string{"praimate-cli"}, Tools: []string{"Write"}, MCPServers: []string{"dangerous"}}
	raw, err := core.MarshalAgentYAML(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.core.ImportAgentYAML(ctx, raw, ""); err != nil {
		t.Fatal(err)
	}
	if err := core.SaveAgentRuntime(source.ID, &core.AgentRuntimeManifest{Schema: core.AgentRuntimeSchema, Mode: core.RuntimeNative, PresetOrigin: core.PresetCustom, Capabilities: core.AgentCapabilities{ReadProject: true, ModifyFiles: true, ExecuteCommands: true, Network: true}, Permissions: core.AgentRuntimePermissions{DefaultTools: "full"}}); err != nil {
		t.Fatal(err)
	}
	action, _ := app.assistantActions().Get("agents.clone")
	if _, err := action.Execute(ctx, map[string]any{"id": "source", "new_id": "reviewer", "name": "Reviewer", "read_only": true}); err != nil {
		t.Fatal(err)
	}
	cloned, err := app.core.GetAgent(ctx, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := core.LoadAgentRuntime("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if len(cloned.Tools) > 0 || len(cloned.MCPServers) > 0 || manifest.Capabilities.ModifyFiles || manifest.Capabilities.ExecuteCommands || manifest.Capabilities.Network || manifest.Permissions.DefaultTools != "plan" {
		t.Fatal("read-only clone retained mutating capabilities")
	}
	original, _ := core.LoadAgentRuntime("source")
	if !original.Capabilities.ModifyFiles {
		t.Fatal("source agent was changed")
	}
}
