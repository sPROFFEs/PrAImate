package launcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/ollama"

	"github.com/sPROFFEs/PrAImate/pkg/targets"
	"github.com/sPROFFEs/PrAImate/pkg/workpath"
)

// PrepareSandbox loads the workpath, validates it, and compiles it into
// the workspace sandbox using the agent's matching wpc target. After this
// returns, the sandbox is ready for the agent to be spawned with that as
// its cwd. We never write into the workpath source — that stays read-only
// as far as the launcher is concerned.
func PrepareSandbox(ws Workspace, agent Agent) error {
	if !agent.Available {
		return fmt.Errorf("agent %s is not installed", agent.ID)
	}

	if err := EnsureSandbox(ws); err != nil {
		return err
	}

	wp, err := workpath.LoadDir(ws.WorkpathDir)
	if err != nil {
		return fmt.Errorf("load workpath: %w", err)
	}
	// The wpc loader defaults Name to the directory's basename, which is
	// always literally "workpath" under our layout. Override so the
	// compiled output is identified by the workspace name (matters for
	// Claude skill dirs, mika modules, and the generic <name>.md target).
	wp.Name = ws.Name
	if err := workpath.Validate(wp); err != nil {
		return fmt.Errorf("validate workpath: %w", err)
	}

	t, err := targets.Get(agent.WpcTarget)
	if err != nil {
		return err
	}
	if err := t.Compile(wp, ws.SandboxDir); err != nil {
		return fmt.Errorf("compile %s into sandbox: %w", agent.WpcTarget, err)
	}

	if err := writeSandboxReadme(ws, agent); err != nil {
		return err
	}

	// Apply per-workspace decorations (language directive, memory
	// staging, online-skill clone, chat-log dir). Errors here are
	// surfaced via the LastDecorationNotes accessor — non-fatal so a
	// flaky network fetch doesn't block the launch.
	notes := applyDecorations(ws, agent)
	// Append a hint for any imported bundle whose underlying tool
	// isn't on PATH yet (e.g. graphify). The note carries the exact
	// `clade -install-tool <name>` command the user should run, so
	// no extra UI work to surface it.
	notes = append(notes, missingImportedToolNotes(wp)...)
	if len(notes) > 0 {
		LastDecorationNotes = notes
	} else {
		LastDecorationNotes = nil
	}
	return nil
}

// LastDecorationNotes is set by PrepareSandbox after each call so the TUI
// can surface non-fatal warnings (online-skill clone failed, memory copy
// failed, etc.) on the launching screen.
var LastDecorationNotes []string

// LastSessionDir holds the per-launch <chat>/sessions/<ts>-<agent>/
// directory recordChatSession most recently created. Set during
// applyDecorations; consumed by main() after the agent exits so the
// post-exit transcript capture knows where to write summary.md /
// transcript.jsonl. Cleared on the next Plan() call.
var LastSessionDir string

// LastSessionStartedAt is the wall-clock moment recordChatSession
// stamped the session manifest. Used by transcript locators as the
// "ignore files modified before this" cutoff so we don't accidentally
// grab a previous session's artifact for the same cwd.
var LastSessionStartedAt time.Time

// contextPrimerRootDoc returns the compiled root-doc filename the given
// agent's wpc target writes to the SANDBOX ROOT — the file that embeds
// this chat's mission, playbook, and rules. Empty for agents we don't
// prime via a positional prompt (opencode/deepseek).
//
//	claude / openclaude → CLAUDE.md   (pkg/targets/claude.go)
//	codex               → AGENTS.md   (pkg/targets/codex.go)
//	gemini              → GEMINI.md   (pkg/targets/gemini.go)
//
// The primer MUST name this file, not the raw playbook.md / rules.md:
// those live in the workpath SOURCE dir (often a sibling of the sandbox,
// not under it) and are never copied into the sandbox — wpc compiles
// them into the root doc instead. The earlier primer told the agent to
// read "playbook.md and rules.md from the current directory", which
// don't exist there, so the agent failed the read 3x and bailed.
func contextPrimerRootDoc(agent AgentID) string {
	switch agent {
	case AgentClaude, AgentOpenClaude:
		return "CLAUDE.md"
	case AgentCodex:
		return "AGENTS.md"
	case AgentGemini:
		return "GEMINI.md"
	}
	return ""
}

// contextPrimerPrompt builds the Option-C primer the launcher passes as
// the agent's first positional argument on fresh launches. It points the
// agent at the root doc that's guaranteed to be in its cwd (which embeds
// the mission/playbook/rules) plus MEMORY.md, then asks for a short ack.
// Returns "" for agents without a root doc / positional-prompt entry.
//
// Kept short — ~50 tokens — so the warm-up cost is negligible. The "say
// so briefly if MEMORY.md is empty" guardrail prevents the agent
// fabricating prior context on a fresh chat.
func contextPrimerPrompt(agent AgentID) string {
	doc := contextPrimerRootDoc(agent)
	if doc == "" {
		return ""
	}
	return "Before doing anything else: read " + doc + " and MEMORY.md in the current directory. " +
		doc + " defines how this chat operates (its mission, playbook, and rules); MEMORY.md is the running log from prior sessions. " +
		"If MEMORY.md is empty or has only headers, say so briefly. " +
		`When done, reply with exactly "Context loaded — ready for your first task." then wait for the user's first message.`
}

// AppendContextPrimer appends the Option-C primer prompt to plan.Args
// when the chat's settings allow it AND the agent supports first-prompt
// injection via a positional CLI argument. Returns the (possibly
// modified) plan. Idempotent: existing positional prompts in plan.Args
// are NOT duplicated — we only append when the trailing args look like
// flags (heuristic), so callers can chain this safely.
//
// Per-agent CLI-arg conventions (Linux man pages + --help):
//
//	claude [PROMPT]               → accepts positional prompt for new chats
//	codex [GLOBAL_FLAGS] [PROMPT] → accepts positional prompt before subcommands
//	codex resume <UUID>           → resume subcommand, no positional prompt
//	gemini [PROMPT]               → typically accepts positional prompt
//	opencode                      → interactive mode has no first-prompt flag
//	deepseek                      → interactive mode has no first-prompt flag
//
// For agents in the "no positional prompt in interactive mode" bucket
// (opencode / deepseek), this is a no-op; those agents rely on Option A
// (AGENTS.md auto-load at session start).
func AppendContextPrimer(plan LaunchPlan, agent Agent, ws Workspace) LaunchPlan {
	if ws.Settings.DisableContextPrimer {
		return plan
	}
	// contextPrimerPrompt returns "" for agents without a positional-
	// prompt entry (opencode/deepseek), so the empty check doubles as
	// the per-agent gate.
	if p := contextPrimerPrompt(agent.ID); p != "" {
		plan.Args = append(plan.Args, p)
	}
	return plan
}

// LastResumeNote / LastResumeRestoredTo carry the most recent native-
// resume attempt's outcome (set by OpenChat after RestoreNativeSession).
// The TUI's launching screen + chat-list diagnostics can render these
// so the user sees "resumed from <path>" or "no prior session" without
// needing a separate UI plumbing pass for every screen.
var LastResumeNote string
var LastResumeRestoredTo string

// LastMirrorResult holds the most recent MirrorOutSlice outcome (set
// by CapturePostExit). LastMirrorInResult is the most recent MirrorIn
// (set by OpenChat when Step 3 mirror-in fires). Both are best-effort
// diagnostics the TUI can surface without changing the plumbing.
var LastMirrorResult MirrorResult
var LastMirrorInResult MirrorResult

// writeSandboxReadme drops an orientation file on first compile so the
// user lands in something self-explanatory when they cd into the sandbox.
func writeSandboxReadme(ws Workspace, agent Agent) error {
	path := filepath.Join(ws.SandboxDir, "SANDBOX.md")
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	body := fmt.Sprintf(`# Sandbox for %s

Knowledge base: %s
Agent: %s (binary: %s)

This directory is the agent's working directory. The launcher compiled the
workpath into it using the %q wpc target, so the agent loads the mission,
playbook, rules, and any tools/subagents on startup.

Anything you create here is gitignored by default. Treat the parent
workpath (../workpath/) as read-only — edit it via the launcher (or by
hand) and re-run the launcher to recompile.
`, ws.Name, ws.WorkpathDir, agent.Label, agent.Binary, agent.WpcTarget)
	return os.WriteFile(path, []byte(body), 0o644)
}

// LaunchPlan describes what the TUI hands off to the OS once Bubble Tea
// has released the terminal: which binary to exec, with what args, in
// which directory, plus optional env overrides.
type LaunchPlan struct {
	Command string
	Args    []string
	Dir     string
	// Env, if non-empty, is merged on top of os.Environ() when spawning
	// the agent. Used for supported local routes and protected credentials.
	Env map[string]string
}

// Plan builds the LaunchPlan but does not actually exec. The TUI layer
// must run Plan first (so the workpath is compiled and any error
// surfaces inside the UI), then quit the Bubble Tea program, then exec
// the plan from main() while the terminal is no longer being driven.
//
// Local routing is supported only for agents whose upstream transport
// accepts OpenAI-compatible endpoints. Claude Code remains on Anthropic;
// OpenClaude is the supported Claude-style local route.
func Plan(ws Workspace, agent Agent) (LaunchPlan, error) {
	// Clear per-launch globals so a partial run on a prior chat
	// doesn't leak state into this one.
	LastSessionDir = ""
	LastSessionStartedAt = time.Time{}
	LastResumeNote = ""
	LastResumeRestoredTo = ""
	LastMirrorResult = MirrorResult{}
	LastMirrorInResult = MirrorResult{}
	if err := PrepareSandbox(ws, agent); err != nil {
		return LaunchPlan{}, err
	}
	plan := LaunchPlan{
		Command: agent.Binary,
		Args:    nil,
		Dir:     ws.SandboxDir,
	}
	o := ws.Settings.Ollama
	ollamaConfigured := o.Endpoint != "" && o.Model != ""
	// Pick the auth token once. Empty APIKey ⇒ "ollama" placeholder
	// (vanilla Ollama ignores it). Non-empty ⇒ real Bearer token for
	// GPUStack / vLLM-with-key / LiteLLM / etc.
	authToken := o.APIKey
	if authToken == "" {
		authToken = "ollama"
	}

	// Per-agent injection is gated by ws.Settings.Ollama.HasAgent(id):
	// "did the user tick this agent in the Local-endpoint wizard?"
	// We check per agent instead of a flat `ollamaConfigured` so a
	// chat configured for codex-only doesn't also inject claude env
	// (and vice versa). The chat-level Ollama block is the source of
	// truth for "this chat opts into the local endpoint at all,"
	// independent of which agents.
	switch agent.ID {
	case AgentClaude:
		if ollamaConfigured && o.HasAgent(AgentClaude) {
			return LaunchPlan{}, errors.New("Claude Code local-LLM routing is not supported; choose OpenClaude for local OpenAI-compatible models, or remove Claude from this chat's local route")
		}
	case AgentOpenClaude:
		if ollamaConfigured && o.HasAgent(AgentOpenClaude) {
			// OpenClaude exposes the OpenAI-compatible code path via
			// CLAUDE_CODE_USE_OPENAI=1. Endpoint + key + model travel
			// through both ~/.openclaude/.openclaude-profile.json and the
			// OPENAI_* env block. OpenClaude expects its OpenAI-compatible
			// base URL to include /v1.
			openAIBaseURL := openAICompatibleBaseURL(o.Endpoint)
			if err := WriteOpenClaudeLocalProfile(o, authToken); err != nil {
				return LaunchPlan{}, fmt.Errorf("openclaude local profile: %w", err)
			}
			plan.Env = map[string]string{
				"CLAUDE_CODE_USE_OPENAI": "1",
				"OPENAI_API_KEY":         authToken,
				"OPENAI_BASE_URL":        openAIBaseURL,
				"OPENAI_API_BASE":        openAIBaseURL,
				"OPENAI_MODEL":           o.Model,
			}
			if raw := openClaudeLimitJSON(o.Model, openAIBaseURL, o.ContextTokens); raw != "" {
				plan.Env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"] = raw
			}
			if raw := openClaudeLimitJSON(o.Model, openAIBaseURL, o.OutputTokens); raw != "" {
				plan.Env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"] = raw
			}
			// The fork inherits Anthropic-flavoured flags too (--model),
			// so we pass it for parity with claude in case env-only
			// model selection loses to a default elsewhere in the chain.
			plan.Args = []string{"--model", o.Model}
		} else {
			// If global local profile is set, we preserve it.
			// Otherwise, we ensure it's removed so it defaults to Anthropic.
			if !IsOpenClaudeConfigured() {
				if err := BackupOpenClaudeLocalProfileIfPresent(); err != nil {
					return LaunchPlan{}, fmt.Errorf("openclaude local profile backup: %w", err)
				}
			}
		}
	case AgentCodex:
		// PrAImate intentionally leaves Codex provider selection and
		// authentication untouched.
	case AgentOpenCode:
		if ollamaConfigured && o.HasAgent(AgentOpenCode) {
			// OpenCode picks up routing from opencode.json. The provider's
			// `options.apiKey` is the primary auth path, but exporting
			// OPENAI_API_KEY too is harmless and covers SDK versions
			// that prefer the env var.
			plan.Env = map[string]string{"OPENAI_API_KEY": authToken}
			plan.Env[ollama.OpenCodeAPIKeyEnv("praimate_local")] = authToken
		}

	case AgentGemini:
		// Intentionally no Ollama handling here. Tried OPENAI_* env
		// vars (the convention that works for Codex/OpenCode) — Gemini
		// CLI 0.42+ continues to hit Google's API via cached OAuth and
		// rejects the Ollama model name with "Model ... was not found".
		// The real switch happens through ~/.gemini/settings.json
		// (selectedAuthType + provider section), whose schema isn't
		// stable across CLI versions. Until we can match the installed
		// version's schema reliably, we leave Gemini launching with
		// its default Google auth — and refuse to pass --model with
		// the Ollama model name, which would just produce the same
		// "not found" error you'd otherwise get.

	case AgentDeepSeek:
		// DeepSeek-TUI reads ~/.deepseek/config.toml at startup, which
		// the Ollama screen wrote (provider="ollama" + base_url + model
		// + optional api_key inline). We still export OPENAI_API_KEY
		// when configured so any sub-tool that reads the env (or a
		// config using `api_key_env = "OPENAI_API_KEY"` instead of an
		// inline key) authenticates correctly.
		if ollamaConfigured && o.HasAgent(AgentDeepSeek) {
			plan.Env = map[string]string{"OPENAI_API_KEY": authToken}
		}
	}
	return plan, nil
}

func openAICompatibleBaseURL(endpoint string) string {
	ep := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if ep == "" {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(ep), "/v1") {
		return ep
	}
	return ep + "/v1"
}

func openClaudeLimitJSON(model, baseURL string, limit int) string {
	if strings.TrimSpace(model) == "" || limit <= 0 {
		return ""
	}
	entries := map[string]int{
		model: limit,
	}
	if host := baseURLHost(baseURL); host != "" {
		entries[host+":"+model] = limit
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(raw)
}

func baseURLHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// PlanWithEnv is Plan + env overrides — the launcher injects these when
// it spawns the agent so users get e.g. Ollama routing without us
// touching their shell rc.
func PlanWithEnv(ws Workspace, agent Agent, env map[string]string) (LaunchPlan, error) {
	plan, err := Plan(ws, agent)
	if err != nil {
		return LaunchPlan{}, err
	}
	plan.Env = env
	return plan, nil
}
