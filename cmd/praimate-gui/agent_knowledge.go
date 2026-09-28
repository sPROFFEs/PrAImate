package main

// PRAIMATE_GPUSTACK_PROVIDER_FIX_V2: generic local endpoints use a temporary Graphify provider.

// Agent knowledge bindings — the GUI side of knowledge packs: pick
// docs (files or a whole folder) into the agent's knowledge dir, set
// the mode (raw folder vs graphify RAG), build/refresh the RAG index
// with streamed output, and import/export .praimate-agent packs.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/installer"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

// RequirementsRunResult is the complete result of an explicitly requested
// environment setup script. A non-zero script exit is reported in Error while
// still returning the captured output and developer instructions to the GUI.
type RequirementsRunResult struct {
	Success      bool   `json:"success"`
	Output       string `json:"output"`
	Error        string `json:"error,omitempty"`
	Instructions string `json:"instructions,omitempty"`
}

// RequirementsProgressEvent streams lifecycle and output updates while an
// explicitly-approved setup script runs.
type RequirementsProgressEvent struct {
	AgentID string `json:"agentID"`
	State   string `json:"state"` // started | output | finished | failed | canceled
	Text    string `json:"text,omitempty"`
	At      int64  `json:"at"`
}

type requirementsProgressWriter struct {
	ctx     context.Context
	agentID string
	tail    tailBuffer
}

func (w *requirementsProgressWriter) Write(p []byte) (int, error) {
	n, err := w.tail.Write(p)
	wruntime.EventsEmit(w.ctx, "praimate:requirements", RequirementsProgressEvent{
		AgentID: w.agentID,
		State:   "output",
		Text:    string(p),
		At:      time.Now().UnixMilli(),
	})
	return n, err
}

func (w *requirementsProgressWriter) String() string {
	return w.tail.String()
}

func requirementsCommand(goos, path string) (string, []string) {
	if goos == "windows" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".cmd", ".bat":
			return "cmd.exe", []string{"/C", path}
		default:
			return "powershell.exe", []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", path}
		}
	}
	return "bash", []string{path}
}

func prepareSudoAskpass(goos string) (string, func(), error) {
	if goos == "windows" {
		return "", func() {}, nil
	}
	for _, candidate := range []string{
		os.Getenv("SUDO_ASKPASS"),
		os.Getenv("SSH_ASKPASS"),
		"ssh-askpass",
		"gnome-ssh-askpass",
		"ksshaskpass",
		"/usr/lib/ssh/ssh-askpass",
	} {
		if candidate == "" {
			continue
		}
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, func() {}, nil
		}
	}

	var body string
	switch goos {
	case "darwin":
		if _, err := os.Stat("/usr/bin/osascript"); err != nil {
			return "", func() {}, nil
		}
		body = `#!/bin/sh
exec /usr/bin/osascript \
  -e 'display dialog "Administrator password required" default answer "" with hidden answer buttons {"OK"} default button "OK" with icon caution' \
  -e 'text returned of result'
`
	default:
		systemdAskpass, err := exec.LookPath("systemd-ask-password")
		if err != nil {
			return "", func() {}, nil
		}
		body = "#!/bin/sh\nexec " + strconv.Quote(systemdAskpass) + " --no-tty --timeout=300 \"$1\"\n"
	}

	f, err := os.CreateTemp("", "praimate-sudo-askpass-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create sudo password helper: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := f.Chmod(0o700); err == nil {
		_, err = io.WriteString(f, body)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write sudo password helper: %w", err)
	}
	return path, cleanup, nil
}

func prepareSudoPopupWrapper(pkexec, realSudo string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "praimate-sudo-popup-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create sudo popup wrapper directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	body := "#!/bin/sh\nexec " + strconv.Quote(pkexec) + " " + strconv.Quote(realSudo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sudo"), []byte(body), 0o700); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("write sudo popup wrapper: %w", err)
	}
	return dir, cleanup, nil
}

type requirementsRun struct {
	cancel context.CancelFunc
}

func (a *App) beginRequirements(id string) (context.Context, func(), error) {
	a.requirementsCancelMu.Lock()
	defer a.requirementsCancelMu.Unlock()
	if _, exists := a.requirementsCancels[id]; exists {
		return nil, nil, fmt.Errorf("requirements script is already running for this agent")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	run := &requirementsRun{cancel: cancel}
	a.requirementsCancels[id] = run
	return ctx, func() {
		cancel()
		a.requirementsCancelMu.Lock()
		if a.requirementsCancels[id] == run {
			delete(a.requirementsCancels, id)
		}
		a.requirementsCancelMu.Unlock()
	}, nil
}

// CancelAgentRequirements stops the active setup script. An already-authorized
// package-manager transaction may finish rather than being killed mid-update.
// It is a no-op when this agent has no requirements run in progress.
func (a *App) CancelAgentRequirements(id string) {
	a.requirementsCancelMu.Lock()
	run := a.requirementsCancels[id]
	a.requirementsCancelMu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// AgentKnowledgeInfo is the knowledge panel's state for one agent.
type AgentKnowledgeInfo struct {
	Mode              string   `json:"mode"` // "", "raw", "rag"
	Dir               string   `json:"dir"`
	Exists            bool     `json:"exists"` // the knowledge folder exists on disk
	Files             []string `json:"files"`
	GraphifyInstalled bool     `json:"graphifyInstalled"`
	HasIndex          bool     `json:"hasIndex"`
	// LocalEndpoint is the saved Local-LLM endpoint (Local LLM tab), so
	// the RAG panel can offer "Local LLM" indexing without retyping it.
	LocalEndpoint string `json:"localEndpoint"`
}

// GetAgentKnowledge reports the agent's knowledge state.
func (a *App) GetAgentKnowledge(id string) (*AgentKnowledgeInfo, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return nil, err
	}
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return nil, err
	}
	files, err := core.ListAgentKnowledge(id)
	if err != nil {
		return nil, err
	}
	_, gOK := installer.ResolveGraphify()
	localCfg, _ := launcher.LoadConfig()
	localEndpoint := ""
	if localCfg != nil {
		localEndpoint = localCfg.DefaultLocalEndpoint
	}
	return &AgentKnowledgeInfo{
		Mode:              agent.Knowledge,
		Dir:               dir,
		Exists:            dirExists(dir),
		Files:             files,
		GraphifyInstalled: gOK,
		HasIndex:          dirExists(dir + "/graphify-out"),
		LocalEndpoint:     localEndpoint,
	}, nil
}

// EnableAgentKnowledge creates the agent's knowledge folder so the user
// can add documents and pick a Raw/RAG mode. Safe to call repeatedly.
func (a *App) EnableAgentKnowledge(id string) error {
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0o755)
}

// SetAgentKnowledgeMode persists the mode ("", "raw", "rag"). The
// documents stay where they are — only the launch guidance changes.
func (a *App) SetAgentKnowledgeMode(id, mode string) error {
	switch mode {
	case "", "raw", "rag":
	default:
		return fmt.Errorf("unknown knowledge mode %q", mode)
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return err
	}
	agent.Knowledge = mode
	raw, err := core.MarshalAgentYAML(agent)
	if err != nil {
		return err
	}
	_, err = c.ImportAgentYAML(a.ctx, raw, agent.SourcePath)
	return err
}

// PickAgentKnowledgeFiles opens a multi-file dialog and copies the
// selection into the agent's knowledge folder. Returns the new list.
func (a *App) PickAgentKnowledgeFiles(id string) ([]string, error) {
	paths, err := wruntime.OpenMultipleFilesDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Add documents to the agent's knowledge base",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Documents", Pattern: "*.md;*.txt;*.pdf;*.csv;*.json;*.yaml;*.html;*.docx"},
			{DisplayName: "All files", Pattern: "*.*"},
		},
	})
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		if _, err := core.AddAgentKnowledgeFiles(id, paths); err != nil {
			return nil, err
		}
	}
	return core.ListAgentKnowledge(id)
}

// PickAgentKnowledgeFolder copies a whole folder into the knowledge base.
func (a *App) PickAgentKnowledgeFolder(id string) ([]string, error) {
	dir, err := wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Add a folder of documents to the agent's knowledge base",
	})
	if err != nil {
		return nil, err
	}
	if dir != "" {
		if _, err := core.AddAgentKnowledgeFiles(id, []string{dir}); err != nil {
			return nil, err
		}
	}
	return core.ListAgentKnowledge(id)
}

// DeleteAgentKnowledgeFile removes one knowledge file.
func (a *App) DeleteAgentKnowledgeFile(id, rel string) ([]string, error) {
	if err := core.DeleteAgentKnowledgeFile(id, rel); err != nil {
		return nil, err
	}
	return core.ListAgentKnowledge(id)
}

// PickAgentRequirementsScript copies a platform-specific setup script into
// the agent's managed directory and records its metadata in agent.yaml. The
// script is not executed here or on import; execution always needs Run.
func (a *App) PickAgentRequirementsScript(id, targetOS, instructions string) (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "Choose requirements script",
		Filters: []wruntime.FileFilter{{DisplayName: "Scripts", Pattern: "*.sh;*.ps1;*.cmd;*.bat"}, {DisplayName: "All files", Pattern: "*.*"}},
	})
	if err != nil || path == "" {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 2<<20 {
		return nil, fmt.Errorf("requirements script is too large (%d bytes; limit is 2 MiB)", info.Size())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return nil, err
	}
	agent.Requirements = &core.AgentRequirements{OS: targetOS, Script: filepath.Base(path), Instructions: strings.TrimSpace(instructions)}
	if err := agent.Validate(); err != nil {
		return nil, err
	}
	if err := core.WriteAgentRequirementsScript(id, agent.Requirements.Script, body); err != nil {
		return nil, err
	}
	raw, err := core.MarshalAgentYAML(agent)
	if err != nil {
		return nil, err
	}
	return c.ImportAgentYAML(a.ctx, raw, agent.SourcePath)
}

// RunAgentRequirements executes the script only after a user presses the
// explicit GUI button. It returns non-zero exits as a result so the UI can
// display both the captured error output and author-provided instructions.
func (a *App) RunAgentRequirements(id string) (*RequirementsRunResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, id)
	if err != nil {
		return nil, err
	}
	if agent.Requirements == nil {
		return nil, fmt.Errorf("agent %q has no requirements script", agent.Name)
	}
	r := agent.Requirements
	if r.OS != runtime.GOOS {
		return nil, fmt.Errorf("this requirements script targets %s; this computer runs %s", r.OS, runtime.GOOS)
	}
	path, err := core.AgentRequirementsScriptPath(id, r.Script)
	if err != nil {
		return nil, err
	}
	runCtx, done, err := a.beginRequirements(id)
	if err != nil {
		return nil, err
	}
	defer done()
	result := &RequirementsRunResult{Instructions: r.Instructions}
	name, args := requirementsCommand(runtime.GOOS, path)
	cmd := exec.CommandContext(runCtx, name, args...)
	overrides := []string{}
	cleanupElevation := func() {}
	pkexec, pkexecErr := exec.LookPath("pkexec")
	realSudo, sudoErr := exec.LookPath("sudo")
	if runtime.GOOS == "linux" && pkexecErr == nil && sudoErr == nil {
		sudoDir, cleanup, wrapErr := prepareSudoPopupWrapper(pkexec, realSudo)
		if wrapErr != nil {
			return nil, wrapErr
		}
		cleanupElevation = cleanup
		overrides = append(overrides, "PATH="+sudoDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		askpass, cleanup, askpassErr := prepareSudoAskpass(runtime.GOOS)
		if askpassErr != nil {
			return nil, askpassErr
		}
		cleanupElevation = cleanup
		if askpass != "" {
			overrides = append(overrides, "SUDO_ASKPASS="+askpass)
		}
	}
	defer cleanupElevation()
	if len(overrides) > 0 {
		cmd.Env = append(os.Environ(), overrides...)
	}
	hideRequirementsTerminal(cmd)
	output := &requirementsProgressWriter{ctx: a.ctx, agentID: id}
	cmd.Stdout = output
	cmd.Stderr = output
	wruntime.EventsEmit(a.ctx, "praimate:requirements", RequirementsProgressEvent{
		AgentID: id,
		State:   "started",
		At:      time.Now().UnixMilli(),
	})
	if err := cmd.Run(); err != nil {
		result.Output = output.String()
		state := "failed"
		result.Error = err.Error()
		if runCtx.Err() != nil {
			state = "canceled"
			result.Error = "requirements script canceled"
		}
		wruntime.EventsEmit(a.ctx, "praimate:requirements", RequirementsProgressEvent{
			AgentID: id,
			State:   state,
			At:      time.Now().UnixMilli(),
		})
		return result, nil
	}
	result.Success = true
	result.Output = output.String()
	wruntime.EventsEmit(a.ctx, "praimate:requirements", RequirementsProgressEvent{
		AgentID: id,
		State:   "finished",
		At:      time.Now().UnixMilli(),
	})
	return result, nil
}

// backendEnvKey maps a graphify backend name to the env var that holds
// its API key. Empty backend == code-only (AST extraction, no key).
var backendEnvKey = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"kimi":      "MOONSHOT_API_KEY",
}

// BuildAgentRAG runs `graphify extract` over the knowledge folder so the
// index at knowledge/graphify-out is ready before the agent's first
// query. backend selects the semantic-extraction LLM:
//
//	""/"code"     — code-only (AST, no key); documents are skipped
//	"claude-cli"  — drives the installed Claude CLI (no key, no cost)
//	"local"       — saved generic OpenAI-compatible Local-LLM endpoint
//	"local-ollama"— saved Ollama endpoint using graphify's Ollama backend
//	other         — a cloud backend taking apiKey
//
// model, when set, overrides graphify's default model (required for
// local backends). graphify resolves from PrAImate's pinned managed
// install first (ResolveGraphify), so our verified version is the
// fallback even if the user's PATH graphify changed. On failure the real
// graphify error is surfaced (not a bare "exit status 1").
func (a *App) BuildAgentRAG(id, backend, apiKey, model string) error {
	graphifyBin, ok := installer.ResolveGraphify()
	if !ok {
		return fmt.Errorf("graphify is not installed — install it from the CLIs tab (Managed tools) first")
	}
	dir, err := core.AgentKnowledgeDir(id)
	if err != nil {
		return err
	}
	if !dirExists(dir) {
		return fmt.Errorf("the agent has no knowledge documents yet — add files first")
	}

	args := []string{"extract", ".", "--token-budget", "4000"}
	env := os.Environ()
	env = append(env, "GRAPHIFY_MAX_OUTPUT_TOKENS=65536")
	switch {
	case backend == "" || backend == "code":
		// code-only — no backend flag, no key.
	case backend == "local" || backend == "local-ollama":
		cfg, _ := launcher.LoadConfig()
		if cfg == nil || strings.TrimSpace(cfg.DefaultLocalEndpoint) == "" {
			return fmt.Errorf("no Local LLM endpoint configured — set one in the Local LLM tab first")
		}

		model = strings.TrimSpace(model)
		if model == "" {
			return fmt.Errorf("local backend needs a model name — enter the exact model name served by the endpoint, e.g. `qwen2.5-coder:7b`")
		}

		key, err := loadLocalLLMAPIKey(a.core)
		if err != nil {
			return fmt.Errorf("load local LLM credential: %w", err)
		}
		key = strings.TrimSpace(key)
		if key == "" {
			// Both the OpenAI SDK and graphify expect a non-empty key,
			// although a normal local Ollama installation ignores it.
			key = "local"
		}

		baseURL := openAIBaseURL(cfg.DefaultLocalEndpoint)
		if backend == "local-ollama" {
			// Use graphify's Ollama-specific backend. It still speaks to
			// Ollama's OpenAI-compatible /v1 endpoint, but adds dynamic
			// context sizing, keep-alive and hollow-response recovery.
			args = append(args,
				"--backend", "ollama",
				"--model", model,
				"--max-concurrency", "1",
			)
			env = append(env,
				"OLLAMA_BASE_URL="+baseURL,
				"OLLAMA_MODEL="+model,
				"OLLAMA_API_KEY="+key,
			)

			// Reuse the capability hints already configured in PrAImate.
			// Leave each value unset when the user has not configured it,
			// allowing graphify to apply its own safe defaults.
			if cfg.DefaultLocalContextTokens > 0 {
				env = append(env,
					"GRAPHIFY_OLLAMA_NUM_CTX="+strconv.Itoa(cfg.DefaultLocalContextTokens),
				)

				outputTokens := cfg.DefaultLocalOutputTokens
				if outputTokens <= 0 {
					outputTokens = 4096
				}
				tokenBudget := cfg.DefaultLocalContextTokens - outputTokens - 1500
				if tokenBudget >= 1024 {
					args = append(args,
						"--token-budget", strconv.Itoa(tokenBudget),
					)
				}
			}
			if cfg.DefaultLocalOutputTokens > 0 {
				env = append(env,
					"GRAPHIFY_MAX_OUTPUT_TOKENS="+strconv.Itoa(cfg.DefaultLocalOutputTokens),
				)
			}
		} else {
			// Graphify's built-in "openai" backend hardcodes api.openai.com
			// and does not honour OPENAI_BASE_URL. Register an ephemeral
			// project-local provider instead, so OpenAI-compatible servers
			// such as GPUStack, vLLM, LM Studio and LocalAI receive the call.
			cleanupProvider, err := writeLocalGraphifyProvider(
				dir, baseURL, model, cfg.DefaultLocalOutputTokens,
			)
			if err != nil {
				return err
			}
			defer cleanupProvider()

			args = append(args,
				"--backend", localGraphifyProviderName,
				"--model", model,
				"--max-concurrency", "1",
			)
			env = append(env,
				"GRAPHIFY_ALLOW_LOCAL_PROVIDERS=1",
				localGraphifyAPIKeyEnv+"="+key,
			)

			if cfg.DefaultLocalContextTokens > 0 {
				outputTokens := cfg.DefaultLocalOutputTokens
				if outputTokens <= 0 {
					outputTokens = 4096
				}
				tokenBudget := cfg.DefaultLocalContextTokens - outputTokens - 1500
				if tokenBudget >= 1024 {
					args = append(args,
						"--token-budget", strconv.Itoa(tokenBudget),
					)
				}
			}
			if cfg.DefaultLocalOutputTokens > 0 {
				env = append(env,
					"GRAPHIFY_MAX_OUTPUT_TOKENS="+strconv.Itoa(cfg.DefaultLocalOutputTokens),
				)
			}
		}
	default:
		args = append(args, "--backend", backend)
		if strings.TrimSpace(model) != "" {
			args = append(args, "--model", strings.TrimSpace(model))
		}
		if key := backendEnvKey[backend]; key != "" && strings.TrimSpace(apiKey) != "" {
			env = append(env, key+"="+strings.TrimSpace(apiKey))
		}
	}

	ctx, done, err := a.beginRAG(id)
	if err != nil {
		return err
	}
	defer done()
	cmd := exec.CommandContext(ctx, graphifyBin, args...)
	hideConsole(cmd)
	cmd.Dir = dir
	cmd.Env = env
	// Stream live AND keep a tail so the failure message is actionable.
	var tail tailBuffer
	w := io.MultiWriter(installLogWriter{ctx: a.ctx, cli: "graphify:" + id}, &tail)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("RAG indexing canceled")
		}
		msg := strings.TrimSpace(tail.String())
		hint := ""
		if strings.Contains(msg, "no LLM API key") || strings.Contains(msg, "requires") || strings.Contains(msg, "semantic") {
			hint = "\n\nDocument/PDF indexing needs an LLM backend + API key (pick one above). " +
				"Or switch this agent to Raw documents — that needs no key (the agent reads the files directly)."
		}
		if msg != "" {
			return fmt.Errorf("graphify extract failed:\n%s%s", lastLines(msg, 12), hint)
		}
		return fmt.Errorf("graphify extract failed: %w%s", err, hint)
	}
	return nil
}

type ragRun struct {
	cancel context.CancelFunc
}

// beginRAG registers one active extraction per agent. The returned cleanup
// removes exactly this run, so an older invocation cannot clear a newer one.
func (a *App) beginRAG(id string) (context.Context, func(), error) {
	a.ragCancelMu.Lock()
	defer a.ragCancelMu.Unlock()
	if _, exists := a.ragCancels[id]; exists {
		return nil, nil, fmt.Errorf("RAG indexing is already running for this agent")
	}
	ctx, cancel := newRAGContext(a.ctx)
	run := &ragRun{cancel: cancel}
	a.ragCancels[id] = run
	return ctx, func() {
		cancel()
		a.ragCancelMu.Lock()
		if a.ragCancels[id] == run {
			delete(a.ragCancels, id)
		}
		a.ragCancelMu.Unlock()
	}, nil
}

// CancelAgentRAG interrupts the active RAG extraction for id. It is a no-op
// when the agent is not currently indexing.
func (a *App) CancelAgentRAG(id string) {
	a.ragCancelMu.Lock()
	run := a.ragCancels[id]
	a.ragCancelMu.Unlock()
	if run != nil {
		run.cancel()
	}
}

// newRAGContext keeps a RAG extraction alive until it completes or the
// application shuts down. Individual LLM requests remain bounded by
// Graphify's API timeout, so an unresponsive model cannot block forever.
func newRAGContext(appCtx context.Context) (context.Context, context.CancelFunc) {
	return context.WithCancel(appCtx)
}

const (
	localGraphifyProviderName = "praimate-local"
	localGraphifyAPIKeyEnv    = "PRAIMATE_LOCAL_API_KEY"
)

// writeLocalGraphifyProvider registers a temporary project-local Graphify
// provider for a generic OpenAI-compatible endpoint. Graphify's built-in
// "openai" backend is intentionally tied to api.openai.com, so setting
// OPENAI_BASE_URL is insufficient. The temporary file contains no API key;
// the key is supplied only through localGraphifyAPIKeyEnv and the original
// providers.json, if one existed, is restored after extraction.
func writeLocalGraphifyProvider(dir, baseURL, model string, outputTokens int) (func(), error) {
	providerDir := filepath.Join(dir, ".graphify")
	providerPath := filepath.Join(providerDir, "providers.json")

	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		return nil, fmt.Errorf("create temporary graphify provider directory: %w", err)
	}

	previous, readErr := os.ReadFile(providerPath)
	hadPrevious := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return nil, fmt.Errorf("read existing graphify providers: %w", readErr)
	}

	provider := map[string]any{
		localGraphifyProviderName: map[string]any{
			"base_url":      baseURL,
			"default_model": model,
			"env_key":       localGraphifyAPIKeyEnv,
			"pricing": map[string]float64{
				"input":  0,
				"output": 0,
			},
			"temperature": 0,
		},
	}
	cfg := provider[localGraphifyProviderName].(map[string]any)
	if outputTokens > 0 {
		cfg["max_tokens"] = outputTokens
		cfg["max_completion_tokens"] = outputTokens
	}

	body, err := json.MarshalIndent(provider, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode temporary graphify provider: %w", err)
	}
	body = append(body, '\n')

	if err := os.WriteFile(providerPath, body, 0o600); err != nil {
		return nil, fmt.Errorf("write temporary graphify provider: %w", err)
	}

	cleanup := func() {
		if hadPrevious {
			_ = os.WriteFile(providerPath, previous, 0o600)
			return
		}
		_ = os.Remove(providerPath)
		_ = os.Remove(providerDir)
	}
	return cleanup, nil
}

// tailBuffer keeps only the last ~8KB written — enough for the error
// tail without unbounded memory on a chatty extract.
type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 8192 {
		t.b = t.b[len(t.b)-8192:]
	}
	return len(p), nil
}
func (t *tailBuffer) String() string { return string(t.b) }

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// openAIBaseURL normalises an OpenAI-compatible endpoint. A bare host gets
// the conventional /v1 path; an explicit path is preserved verbatim because
// servers such as GPUStack may expose compatibility under paths like
// /v1-openai rather than /v1.
func openAIBaseURL(endpoint string) string {
	e := strings.TrimRight(ollama.NormalizeEndpoint(endpoint), "/")

	u, err := url.Parse(e)
	if err != nil || u.Host == "" {
		return e
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1"
	}
	return strings.TrimRight(u.String(), "/")
}

// InstallBundledGraphify downloads PrAImate's bundled standalone
// graphify into the managed bin dir (no Python/uv needed), streaming
// progress over "praimate:install".
func (a *App) InstallBundledGraphify() error {
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Minute)
	defer cancel()
	w := installLogWriter{ctx: a.ctx, cli: "graphify"}
	if err := installer.InstallBundledGraphify(ctx, w); err != nil {
		return err
	}
	refreshManagedPaths()
	return nil
}

// ExportAgentPackDialog saves the agent as a .praimate-agent pack
// (yaml + knowledge, RAG index included) — or bare YAML when the user
// picks that extension.
func (a *App) ExportAgentPackDialog(id string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Export agent pack",
		DefaultFilename: id + core.AgentPackExt,
		Filters: []wruntime.FileFilter{
			{DisplayName: "PrAImate agent pack", Pattern: "*" + core.AgentPackExt},
			{DisplayName: "Agent YAML only", Pattern: "*.yaml;*.yml"},
		},
	})
	if err != nil || path == "" {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(path), ".yaml") || strings.HasSuffix(strings.ToLower(path), ".yml") {
		return path, c.ExportAgent(a.ctx, id, path)
	}
	return path, c.ExportAgentPack(a.ctx, id, path)
}
