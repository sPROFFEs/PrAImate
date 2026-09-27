package main

import (
	"context"
	"path/filepath"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/store"
)

func TestLocalLLMHostsModelsOmitsUnreachableHost(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "test-secret-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{ctx: context.Background(), core: c}
	options, err := app.LocalLLMHostsModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(options) != 0 {
		t.Fatalf("unreachable host was exposed to pickers: %+v", options)
	}
	opt, err := app.LocalLLMModels()
	if err != nil {
		t.Fatal(err)
	}
	if opt.Configured {
		t.Fatalf("unreachable default host remained configured: %+v", opt)
	}
}

func TestMultiHostLocalLLMAndBatchApply(t *testing.T) {
	root := filepath.Join(t.TempDir(), "praimate")
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	st, err := store.InitializeWithPassword(filepath.Join(root, "db.sqlite"), "test-secret-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, _ := core.New(core.Options{Store: st})
	app := &App{ctx: context.Background(), core: c}

	// 1. Initial list should provide default host
	if err := launcher.SaveConfig(&launcher.Config{DefaultLocalEndpoint: "http://127.0.0.1:11434"}); err != nil {
		t.Fatal(err)
	}
	hosts, err := app.ListLocalHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) == 0 || !hosts[0].IsDefault {
		t.Fatalf("expected default host, got: %+v", hosts)
	}

	// 2. Add second host (GPU server)
	gpuHost := LocalHost{
		ID:        "host_gpu_server",
		Name:      "GPU Rig",
		Endpoint:  "http://192.168.1.100:8000",
		APIKey:    "gpu-secret-key",
		IsDefault: false,
	}
	if err := app.SaveLocalHost(gpuHost); err != nil {
		t.Fatal(err)
	}

	hosts, err = app.ListLocalHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
	if _, err := app.ApplyModelsToCLI("praimate-cli", "host_gpu_server", []string{"large", "shared"}); err != nil {
		t.Fatalf("assign GPU models to native CLI: %v", err)
	}
	assignments, err := c.NativeModelAssignments(context.Background())
	if err != nil || len(assignments) != 2 || assignments[0].HostID != "host_gpu_server" {
		t.Fatalf("native CLI assignments: %+v, %v", assignments, err)
	}
	staleHost := gpuHost
	staleHost.Name = "GPU Rig renamed"
	if err := app.SaveLocalHost(staleHost); err != nil {
		t.Fatal(err)
	}
	assignments, err = c.NativeModelAssignments(context.Background())
	if err != nil || len(assignments) != 2 {
		t.Fatalf("editing host erased native assignments: %+v, %v", assignments, err)
	}
	appliedNative, err := app.ListAppliedCLIModels()
	if err != nil {
		t.Fatal(err)
	}
	foundNative := false
	for _, item := range appliedNative {
		if item.CLI == "praimate-cli" && item.HostID == "host_gpu_server" && item.Model == "large" {
			foundNative = true
		}
	}
	if !foundNative {
		t.Fatalf("native model missing from GUI listing: %+v", appliedNative)
	}
	if _, err := app.RemoveModelFromCLI("praimate-cli", "host_gpu_server", "shared"); err != nil {
		t.Fatalf("remove native CLI assignment: %v", err)
	}
	assignments, err = c.NativeModelAssignments(context.Background())
	if err != nil || len(assignments) != 1 || assignments[0].Model != "large" {
		t.Fatalf("native CLI assignment after removal: %+v, %v", assignments, err)
	}

	// 3. Batch apply models to OpenCode for GPU Rig
	models := []string{"qwen2.5-coder:32b", "deepseek-r1:14b"}
	status, err := app.ApplyModelsToCLI("opencode", "host_gpu_server", models)
	if err != nil {
		t.Fatalf("apply models: %v", err)
	}
	if status == "" {
		t.Fatal("expected status message")
	}

	// 4. Verify applied models listing
	applied, err := app.ListAppliedCLIModels()
	if err != nil {
		t.Fatalf("list applied models: %v", err)
	}
	if len(applied) < 2 {
		t.Fatalf("expected at least 2 applied models, got %d", len(applied))
	}

	// 5. Remove one model
	removeStatus, err := app.RemoveModelFromCLI("opencode", "host_gpu_server", "deepseek-r1:14b")
	if err != nil {
		t.Fatalf("remove model: %v", err)
	}
	if removeStatus == "" {
		t.Fatal("expected remove status message")
	}

	appliedAfter, _ := app.ListAppliedCLIModels()
	for _, m := range appliedAfter {
		if m.Model == "deepseek-r1:14b" && m.HostID == "host_gpu_server" {
			t.Fatalf("model was not removed: %+v", appliedAfter)
		}
	}
}
