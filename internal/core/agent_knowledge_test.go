package core

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempConfigDir redirects os.UserConfigDir for the test so
// knowledge folders land in a scratch dir, not the developer's real
// config.
func withTempConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	switch {
	case os.Getenv("XDG_CONFIG_HOME") != "" || os.Getenv("HOME") != "":
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	t.Setenv("AppData", dir) // windows
	return dir
}

func knowledgeAgentYAML(id, mode string) []byte {
	return []byte(`schema: praimate.agent/v1
id: ` + id + `
name: K Agent
instructions: answer from your knowledge
supports: [claude]
knowledge: ` + mode + `
`)
}

// The pack round-trip: export yaml+knowledge as .praimate-agent, wipe,
// import on the "other machine", and the docs + mode arrive intact.
func TestAgentPack_ExportImportRoundTrip(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})

	if _, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("k-agent", "raw"), ""); err != nil {
		t.Fatalf("import yaml: %v", err)
	}
	if _, err := AddAgentKnowledgeFiles("k-agent", []string{writeTempDoc(t, "guide.md", "# Style guide\nrules here")}); err != nil {
		t.Fatalf("add knowledge: %v", err)
	}
	if err := SaveAgentRuntime("k-agent", &AgentRuntimeManifest{
		Schema: AgentRuntimeSchema, PresetOrigin: PresetToolEnabled, Mode: RuntimeNative,
		Capabilities: AgentCapabilities{ReadProject: true, ModifyFiles: true},
		Permissions:  AgentRuntimePermissions{DefaultTools: "edits"},
	}); err != nil {
		t.Fatalf("save runtime: %v", err)
	}

	pack := filepath.Join(t.TempDir(), "k-agent"+AgentPackExt)
	if err := c.ExportAgentPack(ctx, "k-agent", pack); err != nil {
		t.Fatalf("export pack: %v", err)
	}

	// "Other machine": fresh DB, wiped knowledge dir.
	c2, _ := New(Options{Store: openTempStore(t)})
	dir, _ := AgentKnowledgeDir("k-agent")
	_ = os.RemoveAll(dir)

	agent, err := c2.ImportAgentPack(ctx, pack)
	if err != nil {
		t.Fatalf("import pack: %v", err)
	}
	if agent.Knowledge != "raw" {
		t.Errorf("knowledge mode = %q", agent.Knowledge)
	}
	files, err := ListAgentKnowledge("k-agent")
	if err != nil || len(files) != 1 || files[0] != "guide.md" {
		t.Fatalf("knowledge files after import = %v (err %v)", files, err)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "guide.md"))
	if !strings.Contains(string(body), "Style guide") {
		t.Errorf("knowledge content lost: %q", body)
	}
	runtime, err := LoadAgentRuntime("k-agent")
	if err != nil || runtime == nil || runtime.PresetOrigin != PresetToolEnabled || runtime.Permissions.DefaultTools != "edits" {
		t.Fatalf("runtime after import = %#v (err %v)", runtime, err)
	}
}

func TestAgentRuntime_MissingManifestPreservesNativeCompatibility(t *testing.T) {
	withTempConfigDir(t)
	c, _ := New(Options{Store: openTempStore(t)})
	effective, err := c.ResolveEffectiveAgentConfig(context.Background(), &Agent{ID: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	if effective.Mode != RuntimeNative || !effective.NativeCompatible || effective.Manifest != nil {
		t.Fatalf("legacy runtime = %#v", effective)
	}
}

func TestAgentRuntime_StrictValidationRejectsUnknownOrFalseClaims(t *testing.T) {
	for _, body := range []string{
		`{"schema":"praimate.runtime/v1","mode":"native","capabilities":{},"features":{},"permissions":{},"surprise":true}`,
		`{"schema":"praimate.runtime/v1","mode":"native","capabilities":{},"features":{"sandbox":true},"permissions":{}}`,
	} {
		if _, err := ParseAgentRuntime([]byte(body)); err == nil {
			t.Fatalf("invalid runtime accepted: %s", body)
		}
	}
}

func TestGuidedAgentPresetExpansionAndExecutionGate(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})

	toolAgent, err := c.CreateGuidedAgent(ctx, GuidedAgentRequest{
		Name: "Tool Reviewer", Purpose: "Review code and fix confirmed defects.", Preset: PresetToolEnabled,
		Supports: []string{"claude"}, Capabilities: AgentCapabilities{ReadProject: true, ModifyFiles: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: SurfaceChat, Agent: toolAgent, CLI: "claude", Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if effective.Tools != "edits" {
		t.Fatalf("guided tool preset tools = %q, want edits", effective.Tools)
	}
	explicitSafe, err := c.ResolveExecutionConfig(ctx, ExecutionRequest{
		Surface: SurfaceChat, Agent: toolAgent, CLI: "claude", Cwd: t.TempDir(),
		ToolsConfigured: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicitSafe.Tools != "" {
		t.Fatalf("explicit safe tools = %q, want empty", explicitSafe.Tools)
	}

	autonomous, err := c.CreateGuidedAgent(ctx, GuidedAgentRequest{
		Name: "Autonomous Reviewer", Purpose: "Complete long reviews independently.", Preset: PresetAutonomous,
		Supports: []string{"claude"}, Capabilities: AgentCapabilities{ReadProject: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range []ExecutionSurface{SurfaceChat, SurfaceStudio, SurfaceWorkflow, SurfaceTerminal} {
		_, err = c.ResolveExecutionConfig(ctx, ExecutionRequest{Surface: surface, Agent: autonomous, CLI: "claude", Cwd: t.TempDir()})
		want := "only supports native execution"
		if surface == SurfaceTerminal {
			want = "not allowed"
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("agentic runtime did not fail closed on %s: %v", surface, err)
		}
	}
}

func TestGuidedAgentPresetExpansionIsDeterministic(t *testing.T) {
	requested := AgentCapabilities{ReadProject: true, ExecuteCommands: true}
	tests := []struct {
		preset       AgentPreset
		mode         AgentRuntimeMode
		defaultTools string
		features     []string
		warnings     int
		maxChildren  int
	}{
		{preset: PresetSimple, mode: RuntimeNative},
		{preset: PresetToolEnabled, mode: RuntimeNative, defaultTools: "ask"},
		{preset: PresetAutonomous, mode: RuntimeAgentic, features: []string{"managed tools", "working memory", "artifact store", "checkpoints"}, warnings: 1},
		{preset: PresetTeam, mode: RuntimeAgentic, features: []string{"managed tools", "working memory", "sandbox", "artifact store", "checkpoints", "delegation"}, warnings: 1, maxChildren: 4},
	}
	for _, tt := range tests {
		t.Run(string(tt.preset), func(t *testing.T) {
			first, summary, warnings, err := expandAgentPreset(tt.preset, requested)
			if err != nil {
				t.Fatal(err)
			}
			second, _, _, err := expandAgentPreset(tt.preset, requested)
			if err != nil {
				t.Fatal(err)
			}
			firstJSON, _ := MarshalAgentRuntime(first)
			secondJSON, _ := MarshalAgentRuntime(second)
			if string(firstJSON) != string(secondJSON) {
				t.Fatalf("preset expansion is not deterministic:\n%s\n%s", firstJSON, secondJSON)
			}
			if first.Mode != tt.mode || first.Permissions.DefaultTools != tt.defaultTools || first.Features.MaxChildren != tt.maxChildren {
				t.Fatalf("manifest = %#v", first)
			}
			if got := requiredAgenticFeatures(first.Features); strings.Join(got, ",") != strings.Join(tt.features, ",") {
				t.Fatalf("features = %v, want %v", got, tt.features)
			}
			if len(warnings) != tt.warnings || len(summary) == 0 {
				t.Fatalf("summary=%v warnings=%v", summary, warnings)
			}
		})
	}
}

func TestGuidedCreationHidesUnexecutableTeamAndRemovesTerminalFromAutonomous(t *testing.T) {
	if _, err := PreviewGuidedAgent(GuidedAgentRequest{Name: "Team", Purpose: "Delegate work", Preset: PresetTeam}); err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("Team guided preview err = %v", err)
	}
	preview, err := PreviewGuidedAgent(GuidedAgentRequest{Name: "Autonomous", Purpose: "Work independently", Preset: PresetAutonomous})
	if err != nil {
		t.Fatal(err)
	}
	if contains(preview.Agent.Surfaces, "terminal") || !contains(preview.Agent.Surfaces, "chat") || !contains(preview.Agent.Surfaces, "editor") {
		t.Fatalf("Autonomous surfaces = %v", preview.Agent.Surfaces)
	}
}

func TestImportAgentPackWithoutRuntimeRemovesStaleManifest(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})
	if _, err := c.ImportAgentYAML(ctx, knowledgeAgentYAML("replace-agent", ""), ""); err != nil {
		t.Fatal(err)
	}
	if err := SaveAgentRuntime("replace-agent", &AgentRuntimeManifest{Schema: AgentRuntimeSchema, PresetOrigin: PresetSimple, Mode: RuntimeNative}); err != nil {
		t.Fatal(err)
	}
	pack := filepath.Join(t.TempDir(), "replace"+AgentPackExt)
	writeZip(t, pack, map[string]string{"agent.yaml": string(knowledgeAgentYAML("replace-agent", ""))})
	if _, err := c.ImportAgentPack(ctx, pack); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadAgentRuntime("replace-agent")
	if err != nil || manifest != nil {
		t.Fatalf("runtime after replacement = %#v (err %v)", manifest, err)
	}
}

func TestAgentPack_RequirementsScriptRoundTrip(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})

	const yamlBody = `schema: praimate.agent/v1
id: requirements-agent
name: Requirements Agent
instructions: prepare the environment
supports: [claude]
requirements:
  os: linux
  script: setup.sh
  instructions: Install Docker before continuing.
`
	if _, err := c.ImportAgentYAML(ctx, []byte(yamlBody), ""); err != nil {
		t.Fatalf("import yaml: %v", err)
	}
	if err := WriteAgentRequirementsScript("requirements-agent", "setup.sh", []byte("#!/bin/sh\necho ready\n")); err != nil {
		t.Fatalf("write requirements script: %v", err)
	}

	pack := filepath.Join(t.TempDir(), "requirements-agent"+AgentPackExt)
	if err := c.ExportAgentPack(ctx, "requirements-agent", pack); err != nil {
		t.Fatalf("export pack: %v", err)
	}

	c2, _ := New(Options{Store: openTempStore(t)})
	agent, err := c2.ImportAgentPack(ctx, pack)
	if err != nil {
		t.Fatalf("import pack: %v", err)
	}
	if agent.Requirements == nil || agent.Requirements.OS != "linux" || agent.Requirements.Script != "setup.sh" {
		t.Fatalf("requirements metadata = %#v", agent.Requirements)
	}
	body, err := ReadAgentRequirementsScript(agent.ID, agent.Requirements.Script)
	if err != nil || !strings.Contains(string(body), "echo ready") {
		t.Fatalf("requirements script = %q (err %v)", body, err)
	}
}

// The system prompt every surface sends must carry the knowledge
// pointer — raw says "read the files", rag says "graphify query" — and
// stays clean for agents without knowledge.
func TestAgentSystemPrompt_KnowledgeNote(t *testing.T) {
	withTempConfigDir(t)
	a := &Agent{ID: "k-note", Name: "K", Instructions: "be helpful", Supports: []string{"claude"}}

	if got := AgentSystemPrompt(a); got != "be helpful" {
		t.Errorf("no-knowledge prompt = %q", got)
	}

	// Mode set but no folder yet → still clean (don't point at nothing).
	a.Knowledge = "raw"
	if got := AgentSystemPrompt(a); got != "be helpful" {
		t.Errorf("missing-folder prompt = %q", got)
	}

	if _, err := AddAgentKnowledgeFiles("k-note", []string{writeTempDoc(t, "a.md", "x")}); err != nil {
		t.Fatalf("add: %v", err)
	}
	raw := AgentSystemPrompt(a)
	if !strings.Contains(raw, "knowledge base") || !strings.Contains(raw, "file tools") {
		t.Errorf("raw prompt missing pointer: %q", raw)
	}
	a.Knowledge = "rag"
	rag := AgentSystemPrompt(a)
	if !strings.Contains(rag, "praimate knowledge query") || !strings.Contains(rag, "knowledge.query") {
		t.Errorf("rag prompt missing built-in retrieval guidance: %q", rag)
	}
	dir, _ := AgentKnowledgeDir(a.ID)
	if err := os.MkdirAll(filepath.Join(dir, "graphify-out"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "graphify-out", "graph.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := AgentSystemPrompt(a); !strings.Contains(got, "graphify query") {
		t.Errorf("legacy Graphify guidance missing: %q", got)
	}
}

// Zip-slip entries must be rejected, not extracted.
func TestImportAgentPack_RejectsZipSlip(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})

	pack := filepath.Join(t.TempDir(), "evil"+AgentPackExt)
	writeZip(t, pack, map[string]string{
		"agent.yaml":                 string(knowledgeAgentYAML("evil", "raw")),
		"knowledge/../../escape.txt": "pwned",
	})
	if _, err := c.ImportAgentPack(ctx, pack); err == nil {
		t.Fatal("zip-slip entry must fail the import")
	}
}

func TestImportAgentPack_InvalidPackPreservesExistingAgent(t *testing.T) {
	withTempConfigDir(t)
	ctx := context.Background()
	c, _ := New(Options{Store: openTempStore(t)})

	const original = `schema: praimate.agent/v1
id: existing
name: Existing Agent
instructions: keep me
supports: [claude]
requirements:
  os: linux
  script: setup.sh
`
	if _, err := c.ImportAgentYAML(ctx, []byte(original), ""); err != nil {
		t.Fatalf("import original: %v", err)
	}
	if _, err := AddAgentKnowledgeFiles("existing", []string{writeTempDoc(t, "keep.md", "keep knowledge")}); err != nil {
		t.Fatalf("add original knowledge: %v", err)
	}
	if err := WriteAgentRequirementsScript("existing", "setup.sh", []byte("#!/bin/sh\necho keep\n")); err != nil {
		t.Fatalf("write original requirements: %v", err)
	}
	if err := SaveAgentRuntime("existing", &AgentRuntimeManifest{Schema: AgentRuntimeSchema, PresetOrigin: PresetSimple, Mode: RuntimeNative}); err != nil {
		t.Fatalf("write original runtime: %v", err)
	}

	pack := filepath.Join(t.TempDir(), "invalid"+AgentPackExt)
	writeZip(t, pack, map[string]string{
		"agent.yaml": `schema: praimate.agent/v1
id: existing
name: Replacement Agent
instructions: replace me
supports: [claude]
`,
		"knowledge/new.md":          "new knowledge",
		"requirements/../../bad.sh": "escape",
		"runtime.json":              `{"schema":"praimate.runtime/v1","mode":"native","unknown":true}`,
	})
	if _, err := c.ImportAgentPack(ctx, pack); err == nil {
		t.Fatal("invalid pack must fail")
	}

	agent, err := c.GetAgent(ctx, "existing")
	if err != nil {
		t.Fatalf("get preserved agent: %v", err)
	}
	if agent.Name != "Existing Agent" {
		t.Fatalf("agent was changed after failed import: %q", agent.Name)
	}
	dir, _ := AgentKnowledgeDir("existing")
	body, err := os.ReadFile(filepath.Join(dir, "keep.md"))
	if err != nil || string(body) != "keep knowledge" {
		t.Fatalf("knowledge was changed after failed import: %q (err %v)", body, err)
	}
	body, err = ReadAgentRequirementsScript("existing", "setup.sh")
	if err != nil || !strings.Contains(string(body), "echo keep") {
		t.Fatalf("requirements were changed after failed import: %q (err %v)", body, err)
	}
	runtime, err := LoadAgentRuntime("existing")
	if err != nil || runtime == nil || runtime.PresetOrigin != PresetSimple {
		t.Fatalf("runtime was changed after failed import: %#v (err %v)", runtime, err)
	}
}

func writeTempDoc(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}
