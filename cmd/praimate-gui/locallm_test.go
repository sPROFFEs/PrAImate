package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func TestLocalLLMDiscoveryAllowsSlowHostsAndSharesCatalogue(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var probes atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		probes.Add(1)
		time.Sleep(1500 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "provider/model"}}})
	}))
	defer srv.Close()
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: context.Background(), core: c}
	if err := a.SaveLocalHost(LocalHost{ID: "slow", Endpoint: srv.URL}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		hosts, err := a.LocalLLMHostsModels()
		if err != nil || len(hosts) != 1 || len(hosts[0].Models) != 1 {
			t.Fatalf("responsive host disappeared: %v %v", hosts, err)
		}
	}
	if probes.Load() != 1 {
		t.Fatalf("repeated model probes: %d", probes.Load())
	}
}

func TestLocalLLMChangingDefaultKeepsProvidersAndKeys(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: context.Background(), core: c}
	for _, host := range []LocalHost{{ID: "a", Endpoint: "https://a.test", APIKey: "a-key"}, {ID: "b", Endpoint: "https://b.test", APIKey: "b-key"}} {
		if err := a.SaveLocalHost(host); err != nil {
			t.Fatal(err)
		}
		if _, err := a.ApplyModelsToCLI("praimate-code", host.ID, []string{"model"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SetDefaultLocalHost("b"); err != nil {
		t.Fatal(err)
	}
	if key, _ := loadLocalLLMAPIKey(c); key != "b-key" {
		t.Fatal("default kept another host's key")
	}
	if _, err := a.ApplyModelsToCLI("praimate-code", "b", []string{"second"}); err != nil {
		t.Fatal(err)
	}
	items, err := a.ListAppliedCLIModels()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, item := range items {
		found[item.HostID+"/"+item.Model] = true
	}
	if !found["a/model"] || !found["b/model"] || !found["b/second"] {
		t.Fatalf("provider grouping changed with default: %v", found)
	}
	if err := a.DeleteLocalHost("b"); err != nil {
		t.Fatal(err)
	}
	if key, _ := loadLocalLLMAPIKey(c); key != "a-key" {
		t.Fatal("promoted host lost its own key")
	}
	if err := a.SetLocalLLM(LocalLLMDefaults{Endpoint: "https://a.test", RemoveAPIKey: true}); err != nil {
		t.Fatal(err)
	}
	hosts, err := c.ListLocalHosts(context.Background())
	if err != nil || len(hosts) != 1 || hosts[0].HasAPIKey {
		t.Fatal("explicitly removed default key was restored from its backup")
	}
}

func TestLocalLLMAppliedModelsKeepProviderIdentity(t *testing.T) {
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	st, err := store.InitializeWithPassword(filepath.Join(t.TempDir(), "db.sqlite"), "fixture-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	a := &App{ctx: context.Background(), core: c}
	for _, provider := range []string{"provider-a", "provider-b"} {
		if _, err := ollama.ApplyOpenCodeModels(provider, provider, ollama.Settings{Endpoint: "https://same.test/v1"}, []string{"same/model"}, false); err != nil {
			t.Fatal(err)
		}
	}
	items, err := a.ListAppliedCLIModels()
	if err != nil || len(items) != 2 || items[0].ProviderKey != "provider-a" || items[1].ProviderKey != "provider-b" {
		t.Fatalf("providers collapsed or unordered: %v %v", items, err)
	}
	if items[0].HostName != "provider-a" || items[0].Endpoint != "https://same.test/v1" {
		t.Fatal("unregistered provider was mislabeled as the default host")
	}
	if _, err := a.RemoveModelFromCLI("opencode", "missing-host", "same/model"); err == nil {
		t.Fatal("missing host selected a different provider for removal")
	}
	if _, err := a.RemoveAppliedModelFromCLI("opencode", "", "provider-b", "same/model"); err != nil {
		t.Fatal(err)
	}
	items, err = a.ListAppliedCLIModels()
	if err != nil || len(items) != 1 || items[0].ProviderKey != "provider-a" {
		t.Fatalf("removed the wrong provider: %v %v", items, err)
	}
}

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
