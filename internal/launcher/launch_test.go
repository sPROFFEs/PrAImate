package launcher

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chatFromSeededReversing seeds the bundled samples as templates, then
// creates a fresh chat from the "reversing" template — the canonical
// fixture for launch-path tests.
func chatFromSeededReversing(t *testing.T) Chat {
	t.Helper()
	src := samplesDir(t)
	if _, err := os.Stat(src); err != nil {
		t.Skipf("no samples dir at %s", src)
	}
	root := t.TempDir()
	if _, err := SeedSamples(root, []string{src}); err != nil {
		t.Fatal(err)
	}
	tpl, err := LoadTemplate(root, "reversing")
	if err != nil || tpl == nil {
		t.Fatalf("LoadTemplate(reversing): %v %v", err, tpl)
	}
	chat, err := CreateChat(root, *tpl, "test-chat", AgentClaude)
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	return chat
}

func TestPrepareSandbox_CompilesCodexTarget(t *testing.T) {
	chat := chatFromSeededReversing(t)
	ws := chat.AsWorkspace()
	// We compile reversing for Codex even though chat.AgentID is Claude —
	// the test just exercises the codex target path.
	agent := Agent{
		ID: AgentCodex, Label: "Codex CLI", Binary: "codex",
		WpcTarget: "codex", Available: true,
	}
	if err := PrepareSandbox(ws, agent); err != nil {
		t.Fatalf("PrepareSandbox: %v", err)
	}
	for _, rel := range []string{"AGENTS.md", "AGENTS.assets/tools/file_summary.sh", "SANDBOX.md"} {
		if _, err := os.Stat(filepath.Join(ws.SandboxDir, rel)); err != nil {
			t.Errorf("expected %s in sandbox: %v", rel, err)
		}
	}
}

func TestPrepareSandbox_CompilesClaudeTarget(t *testing.T) {
	chat := chatFromSeededReversing(t)
	ws := chat.AsWorkspace()
	agent := Agent{
		ID: AgentClaude, Label: "Claude Code", Binary: "claude",
		WpcTarget: "claude", Available: true,
	}
	if err := PrepareSandbox(ws, agent); err != nil {
		t.Fatalf("PrepareSandbox: %v", err)
	}
	// Claude target writes <sandbox>/.claude/skills/<template>/SKILL.md —
	// the template name flows through AsWorkspace.Name on purpose so two
	// chats cloned from the same template both compile a skill of that
	// name into their respective sandboxes.
	skill := filepath.Join(ws.SandboxDir, ".claude", "skills", "reversing", "SKILL.md")
	if _, err := os.Stat(skill); err != nil {
		t.Errorf("expected Claude skill at %s: %v", skill, err)
	}
}

func TestPlan_RejectsClaudeWhenOllamaConfigured(t *testing.T) {
	chat := chatFromSeededReversing(t)
	chat.Settings = WorkspaceSettings{
		Ollama: OllamaSettings{Endpoint: "http://10.0.0.1:11434", Model: "qwen3"},
	}
	if err := SaveChatSettings(chat); err != nil {
		t.Fatal(err)
	}
	loaded, _ := LoadChat(filepath.Dir(filepath.Dir(chat.Root)), chat.ID)
	ws := loaded.AsWorkspace()
	agent := Agent{ID: AgentClaude, Binary: "claude", WpcTarget: "claude", Available: true}
	_, err := Plan(ws, agent)
	if err == nil || !strings.Contains(err.Error(), "OpenClaude") {
		t.Fatalf("Claude local routing error = %v, want OpenClaude migration guidance", err)
	}
}

// TestPlan_GeminiIgnoresOllamaSettings codifies the intentional
// behaviour after the rollback: Gemini CLI 0.42 ignored our OPENAI_*
// env injection and stayed on Google OAuth, then errored with "Model X
// not found". Until we can write a matching ~/.gemini/settings.json
// per-version, Gemini launches plain — no env, no --model — so the
// user sees a working Gemini and clear breadcrumbs from the Ollama
// screen on how to wire it up by hand.
func TestPlan_GeminiIgnoresOllamaSettings(t *testing.T) {
	t.Skip("Gemini CLI support has been removed")
	chat := chatFromSeededReversing(t)
	chat.Settings = WorkspaceSettings{
		Ollama: OllamaSettings{Endpoint: "http://10.0.0.1:11434", Model: "qwen3"},
	}
	_ = SaveChatSettings(chat)
	loaded, _ := LoadChat(filepath.Dir(filepath.Dir(chat.Root)), chat.ID)
	ws := loaded.AsWorkspace()
	agent := Agent{ID: AgentGemini, Binary: "gemini", WpcTarget: "gemini", Available: true}
	plan, err := Plan(ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 0 {
		t.Errorf("Gemini plan should have no Args (we don't pass --model with an Ollama name); got %v", plan.Args)
	}
	if len(plan.Env) != 0 {
		t.Errorf("Gemini plan should have no Env (OPENAI_* injection doesn't work); got %v", plan.Env)
	}
}

// TestPlan_PerAgentGatingViaHasAgent: the chat-level Ollama block
// can opt some agents in and others out via the Agents list. A chat
// configured for codex-only should NOT inject claude env if you swap
// the chat's agent to claude.
func TestPlan_PerAgentGatingViaHasAgent(t *testing.T) {
	chat := chatFromSeededReversing(t)
	chat.Settings = WorkspaceSettings{
		Ollama: OllamaSettings{
			Endpoint: "http://10.0.0.1:11434", Model: "qwen3",
			Agents: []string{"codex"}, // codex opted in, claude NOT
		},
	}
	_ = SaveChatSettings(chat)
	loaded, _ := LoadChat(filepath.Dir(filepath.Dir(chat.Root)), chat.ID)
	ws := loaded.AsWorkspace()

	// Codex local routing is intentionally unsupported.
	codex := Agent{ID: AgentCodex, Binary: "codex", WpcTarget: "codex", Available: true}
	plan, err := Plan(ws, codex)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 0 || len(plan.Env) != 0 {
		t.Errorf("codex local settings must not affect launch: %+v", plan)
	}

	// Claude: same chat, same Ollama block, but claude is NOT in
	// Agents — should NOT get ANTHROPIC_* injection.
	claude := Agent{ID: AgentClaude, Binary: "claude", WpcTarget: "claude", Available: true}
	plan2, err := Plan(ws, claude)
	if err != nil {
		t.Fatal(err)
	}
	if plan2.Env["ANTHROPIC_BASE_URL"] != "" {
		t.Errorf("claude should NOT get ANTHROPIC_BASE_URL when not in Agents; got %q",
			plan2.Env["ANTHROPIC_BASE_URL"])
	}
	if len(plan2.Args) != 0 {
		t.Errorf("claude should NOT get --model when not in Agents; got %v", plan2.Args)
	}
}

// TestOllamaSettings_HasAgentBackwardCompat: chats created before
// Agents existed have no Agents field (empty slice on load). HasAgent
// should report true for claude (matches the old wizard's
// "save chat-level Ollama only when claude is ticked" behavior).
func TestOllamaSettings_HasAgentBackwardCompat(t *testing.T) {
	legacy := OllamaSettings{Endpoint: "http://x", Model: "m"} // no Agents
	if !legacy.HasAgent(AgentClaude) {
		t.Error("legacy chats with empty Agents should default to claude opted in")
	}
	if legacy.HasAgent(AgentCodex) {
		t.Error("legacy chats should NOT default to other agents opted in")
	}
	// Empty endpoint → nobody's in.
	empty := OllamaSettings{}
	if empty.HasAgent(AgentClaude) {
		t.Error("empty OllamaSettings has nobody opted in")
	}
}

func TestPlan_LeavesCodexProviderUntouchedWhenLocalEndpointConfigured(t *testing.T) {
	chat := chatFromSeededReversing(t)
	chat.Settings = WorkspaceSettings{
		// Agents must include "codex" — Plan() gates per-agent
		// injection on the chat's HasAgent() check now, so a chat
		// that opted into Ollama for claude only would NOT also
		// inject for codex.
		Ollama: OllamaSettings{
			Endpoint: "http://10.0.0.1:11434", Model: "qwen3",
			Agents: []string{"codex"},
		},
	}
	_ = SaveChatSettings(chat)
	loaded, _ := LoadChat(filepath.Dir(filepath.Dir(chat.Root)), chat.ID)
	ws := loaded.AsWorkspace()
	agent := Agent{ID: AgentCodex, Binary: "codex", WpcTarget: "codex", Available: true}
	plan, err := Plan(ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Args) != 0 || len(plan.Env) != 0 {
		t.Errorf("Codex local settings must not affect launch: %+v", plan)
	}
	// Codex routes via config.toml, not env — Plan should NOT inject ANTHROPIC_*.
	if plan.Env["ANTHROPIC_BASE_URL"] != "" {
		t.Errorf("Codex shouldn't get ANTHROPIC_BASE_URL env injection; got %q", plan.Env["ANTHROPIC_BASE_URL"])
	}
}

func TestPlan_OpenClaudeUsesOpenAICompatibleEndpointAndLimits(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	chat := chatFromSeededReversing(t)
	chat.Settings = WorkspaceSettings{
		Ollama: OllamaSettings{
			Endpoint:      "http://10.0.0.1:8000",
			Model:         "qwen3",
			Agents:        []string{"openclaude"},
			ContextTokens: 4096,
			OutputTokens:  1024,
		},
	}
	_ = SaveChatSettings(chat)
	loaded, _ := LoadChat(filepath.Dir(filepath.Dir(chat.Root)), chat.ID)
	ws := loaded.AsWorkspace()
	agent := Agent{ID: AgentOpenClaude, Binary: "openclaude", WpcTarget: "claude", Available: true}
	plan, err := Plan(ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Env["CLAUDE_CODE_USE_OPENAI"] != "1" {
		t.Errorf("CLAUDE_CODE_USE_OPENAI = %q", plan.Env["CLAUDE_CODE_USE_OPENAI"])
	}
	if plan.Env["OPENAI_BASE_URL"] != "http://10.0.0.1:8000/v1" {
		t.Errorf("OPENAI_BASE_URL = %q", plan.Env["OPENAI_BASE_URL"])
	}
	if plan.Env["OPENAI_API_BASE"] != "http://10.0.0.1:8000/v1" {
		t.Errorf("OPENAI_API_BASE = %q", plan.Env["OPENAI_API_BASE"])
	}
	if plan.Env["OPENAI_MODEL"] != "qwen3" {
		t.Errorf("OPENAI_MODEL = %q", plan.Env["OPENAI_MODEL"])
	}
	if _, ok := plan.Env["HOME"]; ok {
		t.Errorf("HOME override should not be set for openclaude local profile routing; Env=%v", plan.Env)
	}
	if _, ok := plan.Env["USERPROFILE"]; ok {
		t.Errorf("USERPROFILE override should not be set for openclaude local profile routing; Env=%v", plan.Env)
	}
	raw, err := os.ReadFile(openClaudeProfilePath(tmpHome))
	if err != nil {
		t.Fatalf("read OpenClaude local profile: %v", err)
	}
	var profile openClaudeProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatalf("parse OpenClaude profile: %v\n%s", err, raw)
	}
	if profile.Profile != "openai" {
		t.Errorf("profile = %q, want openai", profile.Profile)
	}
	if profile.Env["OPENAI_BASE_URL"] != "http://10.0.0.1:8000/v1" {
		t.Errorf("profile OPENAI_BASE_URL = %q", profile.Env["OPENAI_BASE_URL"])
	}
	if profile.Env["OPENAI_MODEL"] != "qwen3" {
		t.Errorf("profile OPENAI_MODEL = %q", profile.Env["OPENAI_MODEL"])
	}
	if profile.Env["OPENAI_API_KEY"] != "ollama" {
		t.Errorf("profile OPENAI_API_KEY = %q", profile.Env["OPENAI_API_KEY"])
	}
	for key, want := range map[string]string{
		"CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS":   `"qwen3":4096`,
		"CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS": `"qwen3":1024`,
	} {
		if !strings.Contains(plan.Env[key], want) ||
			!strings.Contains(plan.Env[key], `"10.0.0.1:8000:qwen3"`) {
			t.Errorf("%s = %q", key, plan.Env[key])
		}
		if !strings.Contains(profile.Env[key], want) ||
			!strings.Contains(profile.Env[key], `"10.0.0.1:8000:qwen3"`) {
			t.Errorf("profile %s = %q", key, profile.Env[key])
		}
	}
	if !equalStrings(plan.Args, []string{"--model", "qwen3"}) {
		t.Errorf("Args = %v, want [--model qwen3]", plan.Args)
	}
}

func TestPlan_OpenClaudeWithoutLocalLLMDoesNotIsolateHome(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	profilePath := openClaudeProfilePath(tmpHome)
	if err := os.MkdirAll(filepath.Dir(profilePath), 0o700); err != nil {
		t.Fatal(err)
	}
	oldProfile := []byte(`{"profile":"openai"}` + "\n")
	if err := os.WriteFile(profilePath, oldProfile, 0o600); err != nil {
		t.Fatal(err)
	}
	chat := chatFromSeededReversing(t)
	ws := chat.AsWorkspace()
	agent := Agent{ID: AgentOpenClaude, Binary: "openclaude", WpcTarget: "claude", Available: true}
	plan, err := Plan(ws, agent)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Env) != 0 {
		t.Errorf("plain openclaude launch should not override env; got %v", plan.Env)
	}
	if _, err := os.Stat(filepath.Join(ws.Root, ".openclaude-home")); !os.IsNotExist(err) {
		t.Errorf("plain openclaude launch should not create managed home; stat err = %v", err)
	}
	if _, err := os.Stat(profilePath); !os.IsNotExist(err) {
		t.Errorf("plain openclaude launch should rename local profile away; stat err = %v", err)
	}
	raw, err := os.ReadFile(openClaudeProfileBackupPath(tmpHome))
	if err != nil {
		t.Fatalf("read profile backup: %v", err)
	}
	if string(raw) != string(oldProfile) {
		t.Errorf("backup profile = %q, want %q", string(raw), string(oldProfile))
	}
}

func TestPlan_NoExtraArgsWhenOllamaNotConfigured(t *testing.T) {
	chat := chatFromSeededReversing(t)
	ws := chat.AsWorkspace()
	for _, agent := range []Agent{
		{ID: AgentClaude, Binary: "claude", WpcTarget: "claude", Available: true},
		{ID: AgentCodex, Binary: "codex", WpcTarget: "codex", Available: true},
	} {
		plan, err := Plan(ws, agent)
		if err != nil {
			t.Fatalf("Plan(%s): %v", agent.ID, err)
		}
		if len(plan.Args) != 0 {
			t.Errorf("%s without ollama should have no Args; got %v", agent.ID, plan.Args)
		}
	}
}

func TestPrepareSandbox_RefusesUnavailableAgent(t *testing.T) {
	root := t.TempDir()
	tpl, err := CreateTemplate(root, "empty", "x")
	if err != nil {
		t.Fatal(err)
	}
	chat, err := CreateChat(root, tpl, "x-chat", AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	agent := Agent{ID: AgentCodex, Binary: "codex", WpcTarget: "codex", Available: false}
	if err := PrepareSandbox(chat.AsWorkspace(), agent); err == nil {
		t.Error("expected error for unavailable agent")
	}
}

func TestDetectAgents_PopulatesEntries(t *testing.T) {
	// We can't assume anything about which CLIs are installed on the test
	// host; just check the catalog comes back with the expected IDs and
	// that Available is a deterministic bool (not panicking, etc.).
	agents := DetectAgents(t.Context())
	if len(agents) != 8 {
		t.Fatalf("expected 8 agents, got %d", len(agents))
	}
	wantIDs := map[AgentID]bool{
		AgentClaude: true, AgentOpenClaude: true, AgentCodex: true,
		AgentOpenCode: true, AgentPraimateCode: true, AgentPraimateCLI: true,
		AgentCopilot: true, AgentAntigravity: true,
	}
	for _, a := range agents {
		if !wantIDs[a.ID] {
			t.Errorf("unexpected agent ID %q", a.ID)
		}
		if a.Binary == "" || a.WpcTarget == "" {
			t.Errorf("agent %s has empty binary or target", a.ID)
		}
	}
}

// TestContextPrimer_NamesRootDocNotWorkpathFiles pins the fix for the
// primer pointing at files that aren't in the sandbox. playbook.md and
// rules.md live in the workpath SOURCE dir (a sibling of the sandbox),
// never in the agent's cwd — wpc compiles them into the per-target root
// doc (CLAUDE.md / AGENTS.md / GEMINI.md). The primer must name that
// root doc, not the raw workpath filenames. The OLD primer said "read
// MEMORY.md, playbook.md, and rules.md from the current directory",
// which made the agent fail the read 3x and bail.
func TestContextPrimer_NamesRootDocNotWorkpathFiles(t *testing.T) {
	cases := map[AgentID]string{
		AgentClaude:     "CLAUDE.md",
		AgentOpenClaude: "CLAUDE.md",
		AgentCodex:      "AGENTS.md",
		AgentGemini:     "GEMINI.md",
	}
	for agent, wantDoc := range cases {
		p := contextPrimerPrompt(agent)
		if p == "" {
			t.Errorf("%s: primer empty, want it to name %s", agent, wantDoc)
			continue
		}
		if !strings.Contains(p, wantDoc) {
			t.Errorf("%s: primer doesn't name root doc %s: %q", agent, wantDoc, p)
		}
		// Must NOT instruct reading the non-existent standalone files.
		for _, bad := range []string{"playbook.md", "rules.md"} {
			if strings.Contains(p, bad) {
				t.Errorf("%s: primer references %s, which isn't in the sandbox: %q", agent, bad, p)
			}
		}
		// Should still point at MEMORY.md (which IS at the sandbox root).
		if !strings.Contains(p, "MEMORY.md") {
			t.Errorf("%s: primer no longer mentions MEMORY.md: %q", agent, p)
		}
	}
	// Agents without a positional-prompt entry get no primer.
	for _, agent := range []AgentID{AgentOpenCode, AgentDeepSeek} {
		if p := contextPrimerPrompt(agent); p != "" {
			t.Errorf("%s: expected no primer, got %q", agent, p)
		}
	}
}
