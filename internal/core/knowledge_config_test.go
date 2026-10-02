package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/knowledge"
)

func TestKnowledgeConfigPortableWithoutCredentials(t *testing.T) {
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	if _, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("remote-agent", "rag"), ""); err != nil {
		t.Fatal(err)
	}
	cfg := AgentKnowledgeConfig{Source: "remote", Endpoint: "https://example.test/knowledge"}
	if _, err := c.SaveAgentKnowledgeConfig(ctx, "remote-agent", cfg, "private-token", false); err != nil {
		t.Fatal(err)
	}
	agent, err := c.GetAgent(ctx, "remote-agent")
	if err != nil || !agent.HasKnowledgeAPIKey() {
		t.Fatalf("saved key: %v", err)
	}
	raw, err := MarshalAgentYAML(agent)
	if err != nil {
		t.Fatal(err)
	}
	jsonBody, _ := json.Marshal(agent)
	if strings.Contains(string(raw)+string(jsonBody), "private-token") || !strings.Contains(string(raw), "knowledge_config:") {
		t.Fatal("credential exported or config lost")
	}
	parsed, err := ParseAgentYAML(strings.NewReader(string(raw)))
	if err != nil || !parsed.KnowledgeConfig.Remote() {
		t.Fatalf("portable config: %v", err)
	}
	markdown, err := MarshalAgentMarkdown(agent)
	if err != nil || strings.Contains(string(markdown), "private-token") {
		t.Fatal("Markdown exported credential", err)
	}
	converted, err := ParseAgentMarkdown(strings.NewReader(string(markdown)), "remote-agent.md")
	if err != nil || !converted.KnowledgeConfig.Remote() || converted.KnowledgeConfig.Endpoint != cfg.Endpoint {
		t.Fatalf("Markdown knowledge conversion: %+v %v", converted, err)
	}
	cfg.Endpoint = "https://other.test"
	if _, err = c.SaveAgentKnowledgeConfig(ctx, "remote-agent", cfg, "", false); err != nil {
		t.Fatal(err)
	}
	agent, _ = c.GetAgent(ctx, "remote-agent")
	if agent.HasKnowledgeAPIKey() {
		t.Fatal("credential reused on changed endpoint")
	}
	if err = c.DeleteAgent(ctx, "remote-agent"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{knowledgeConfigKey("remote-agent"), knowledgeAPIKeySetting("remote-agent")} {
		raw, err := c.GetSetting(ctx, ScopeCLI, key)
		if err != nil || len(raw) > 0 {
			t.Fatalf("orphaned config: %s %s %v", key, raw, err)
		}
	}
}

func TestRemoteAgentKnowledgeUsesSavedKeyAndApproval(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	_, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("remote-tools", "rag"), "")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("saved credential missing")
		}
		_ = json.NewEncoder(w).Encode(knowledge.RemoteResponse{Status: "ok", Text: "[guide.md:1] Server source excerpt"})
	}))
	defer server.Close()
	_, err = c.SaveAgentKnowledgeConfig(ctx, "remote-tools", AgentKnowledgeConfig{Source: "remote", Endpoint: server.URL}, "test-key", false)
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := c.GetAgent(ctx, "remote-tools")
	if err = validateManagedKnowledge(agent); err != nil {
		t.Fatalf("remote required a local corpus: %v", err)
	}
	broker, err := newManagedToolBroker(ctx, agent, AgentCapabilities{}, t.TempDir(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer broker.Close()
	if _, err = broker.ExecuteTool(ctx, "knowledge.query", []byte(`{"question":"source"}`)); err == nil || calls.Load() != 0 {
		t.Fatal("remote request bypassed approval")
	}
	broker.approval = &ApprovalConfig{Request: func(ctx context.Context, tool string, input map[string]any) (bool, error) {
		raw, _ := json.Marshal(input)
		if strings.Contains(string(raw), "test-key") {
			t.Fatal("key exposed in approval")
		}
		return true, nil
	}}
	result, err := broker.ExecuteTool(ctx, "knowledge.query", []byte(`{"question":"source"}`))
	if err != nil || !strings.Contains(result, "Server source") {
		t.Fatalf("remote query: %q %v", result, err)
	}
	if !strings.Contains(AgentSystemPrompt(agent), "--agent remote-tools") || strings.Contains(AgentSystemPrompt(agent), "test-key") {
		t.Fatal("remote native prompt lost route or exposed credential")
	}
}

func TestKnowledgeModeAndConfigurationSaveTogether(t *testing.T) {
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	if _, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("mode-config", ""), ""); err != nil {
		t.Fatal(err)
	}
	cfg := AgentKnowledgeConfig{Source: "remote", Endpoint: "https://example.test/knowledge"}
	if _, err := c.SaveAgentKnowledgeConfig(ctx, "mode-config", cfg, "mode-key", false, "invalid"); err == nil {
		t.Fatal("invalid mode was accepted")
	}
	agent, err := c.GetAgent(ctx, "mode-config")
	if err != nil || agent.KnowledgeConfig != nil || agent.HasKnowledgeAPIKey() || agent.Knowledge != "" {
		t.Fatalf("invalid mode partially saved configuration: %+v %v", agent, err)
	}
	if _, err = c.SaveAgentKnowledgeConfig(ctx, "mode-config", cfg, "mode-key", false, "raw"); err != nil {
		t.Fatal(err)
	}
	agent, err = c.GetAgent(ctx, "mode-config")
	if err != nil || !agent.HasKnowledgeAPIKey() || agent.Knowledge != "raw" || !agent.KnowledgeConfig.Remote() {
		t.Fatalf("mode/configuration not saved together: %+v %v", agent, err)
	}
	prompt := AgentSystemPrompt(agent)
	if strings.Contains(prompt, "knowledge.query") || !strings.Contains(prompt, "knowledge.read") {
		t.Fatal("raw mode instructed the model to use an unavailable RAG tool")
	}
	if _, err = c.SaveAgentKnowledgeConfig(ctx, "mode-config", cfg, "", false, "rag"); err != nil {
		t.Fatal(err)
	}
	agent, _ = c.GetAgent(ctx, "mode-config")
	if !agent.HasKnowledgeAPIKey() || !strings.Contains(AgentSystemPrompt(agent), "knowledge.query") {
		t.Fatal("switching to RAG lost the credential or query guidance")
	}
}

type knowledgeExtractionAdapter struct {
	calls int
	opts  SingleShotOpts
}

func (*knowledgeExtractionAdapter) Name() string                    { return "codex" }
func (*knowledgeExtractionAdapter) Available(context.Context) error { return nil }
func (*knowledgeExtractionAdapter) ManagedSafeMode() bool           { return true }
func (*knowledgeExtractionAdapter) SupportsResume() bool            { return false }
func (*knowledgeExtractionAdapter) Resume(context.Context, string, ResumeOpts) (*Reply, error) {
	return nil, errors.New("unexpected resume")
}
func (a *knowledgeExtractionAdapter) SingleShot(ctx context.Context, opts SingleShotOpts) (*Reply, error) {
	a.calls++
	a.opts = opts
	files, err := os.ReadDir(opts.Cwd)
	if err != nil || len(files) != 0 {
		return nil, errors.New("enrichment workspace was not empty")
	}
	return &Reply{Text: `{"keywords":["authentication"],"entities":[],"relationships":[]}`}, nil
}
func (a *knowledgeExtractionAdapter) SingleShotStream(ctx context.Context, opts SingleShotOpts, emit StreamHandler) (*Reply, error) {
	emit(StreamEvent{Type: "usage", Usage: &NativeUsage{PromptTokens: 100, CompletionTokens: 20}})
	return a.SingleShot(ctx, opts)
}

func TestAgentKnowledgeEnrichmentUsesExistingAdapterAndUsage(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	_, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("semantic-agent", "rag"), "")
	if err != nil {
		t.Fatal(err)
	}
	dir, _ := AgentKnowledgeDir("semantic-agent")
	_ = os.MkdirAll(dir, 0700)
	_ = os.WriteFile(filepath.Join(dir, "guide.md"), []byte("Signed bearer credentials allow login."), 0600)
	adapter := &knowledgeExtractionAdapter{}
	old, _ := GetCLIAdapter("codex")
	RegisterCLIAdapter(adapter)
	defer func() {
		if old != nil {
			RegisterCLIAdapter(old)
		} else {
			UnregisterCLIAdapter("codex")
		}
	}()
	cfg := AgentKnowledgeConfig{Source: "local", EnrichmentCLI: "codex", EnrichmentModel: "configured-model", MaxEnrichmentChunks: 1}
	if _, err = c.SaveAgentKnowledgeConfig(ctx, "semantic-agent", cfg, "", false); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = c.BuildAgentKnowledge(ctx, "semantic-agent"); err != nil {
			t.Fatal(err)
		}
	}
	if adapter.calls != 1 || adapter.opts.Model != "configured-model" || adapter.opts.Tools != "" || adapter.opts.Cwd == dir {
		t.Fatalf("incorrect extraction route: %+v", adapter)
	}
	text, err := c.AgentKnowledgeRequest(ctx, "semantic-agent", "query", map[string]any{"question": "authentication"})
	if err != nil || !strings.Contains(text, "guide.md:1") {
		t.Fatalf("enriched source query: %q %v", text, err)
	}
	usage, err := c.UsageDashboard(ctx, time.Now().UTC().Format("2006-01"))
	if err != nil || usage.Totals.ReportedRuns != 1 || usage.Totals.Tokens != 120 {
		t.Fatalf("enrichment usage missing: %+v %v", usage, err)
	}
	cfg.EnrichmentCLI = ""
	cfg.EnrichmentModel = ""
	_, err = c.SaveAgentKnowledgeConfig(ctx, "semantic-agent", cfg, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.BuildAgentKnowledge(ctx, "semantic-agent"); err != nil {
		t.Fatal(err)
	}
	text, err = c.AgentKnowledgeRequest(ctx, "semantic-agent", "query", map[string]any{"question": "authentication"})
	if err != nil || strings.Contains(text, "guide.md:1") || adapter.calls != 1 {
		t.Fatalf("disabled enrichment remained active: %q %v", text, err)
	}
}
