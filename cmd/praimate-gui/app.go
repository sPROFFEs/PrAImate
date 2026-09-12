package main

// App is the Wails binding surface. Every exported method becomes a
// JS-callable function under window.go.main.App.*. All methods are
// thin delegations into internal/core — the GUI holds NO business
// logic (plan §1 iron rule).
//
// Error convention: methods return (T, error); Wails surfaces the
// error as a rejected promise so the frontend can toast it.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"git.jtsec.local/lab/PrAImate/internal/backup"
	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/installer"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
	"git.jtsec.local/lab/PrAImate/internal/store"
	"git.jtsec.local/lab/PrAImate/internal/version"
)

// App carries the shared Core plus the Wails context used for dialogs
// and event emission.
type App struct {
	ctx      context.Context
	st       *store.Store
	core     *core.Core
	dbPath   string
	initErr  string
	quit     func(context.Context)
	unlockMu sync.Mutex

	// daemonMu guards the daemon handles — Wails dispatches each
	// binding call on its own goroutine, so two watcher mutations can
	// race on restartWatchers without it.
	daemonMu  sync.Mutex
	watchers  *core.WatcherDaemon
	schedules *core.ScheduleDaemon

	terms *termManager

	// chatCancels maps chatID → cancel func for the in-flight streamed
	// turn, so the Stop button can interrupt it. Guarded by chatCancelMu
	// (binding calls run on independent goroutines).
	chatCancelMu    sync.Mutex
	chatCancels     map[string]context.CancelFunc
	chatCancelIDs   map[string]uint64
	chatCancelSeq   uint64
	managedCancelMu sync.Mutex
	managedCancels  map[string]context.CancelFunc

	// ragCancels maps agent IDs to active graphify extractions so the RAG
	// controls can stop only their own child process. Guarded by ragCancelMu.
	ragCancelMu sync.Mutex
	ragCancels  map[string]*ragRun

	// requirementsCancels maps agent IDs to active requirements scripts so
	// their live progress panel can stop only the matching run.
	requirementsCancelMu sync.Mutex
	requirementsCancels  map[string]*requirementsRun

	resetMu   sync.Mutex
	dataReset bool

	// approval is the lazily-started mid-turn approval broker ("ask"
	// Tools level). Guarded by approvalMu.
	approvalMu sync.Mutex
	approval   *approvalBroker

	// skillsBroker is the internal MCP skills server for dynamic on-demand
	// skill loading in CLIs and sessions. Guarded by skillsBrokerMu.
	skillsBrokerMu sync.Mutex
	skillsBroker   *skillsBroker

	// editorOwnWrites suppresses fsnotify echoes of the studio's own
	// file flushes (editor_window.go). Guarded by editorMu.
	editorMu        sync.Mutex
	editorScopeMu   sync.Mutex
	editorOwnWrites map[string]time.Time

	// detached coordinates lightweight chat/terminal windows. Only the main
	// process owns it; detached child processes use detachedClient and never
	// open the database or start background daemons.
	detached       *detachedCoordinator
	detachedClient *detachedClient
}

func NewApp() *App {
	a := &App{
		terms:               newTermManager(),
		chatCancels:         map[string]context.CancelFunc{},
		chatCancelIDs:       map[string]uint64{},
		managedCancels:      map[string]context.CancelFunc{},
		ragCancels:          map[string]*ragRun{},
		requirementsCancels: map[string]*requirementsRun{},
		quit:                wruntime.Quit,
	}
	if detachedProcessMode.active {
		a.terms = nil
		a.detachedClient = newDetachedClient(detachedProcessMode)
	} else {
		a.detached = newDetachedCoordinator(a)
	}
	return a
}

// startup opens the shared DB and seeds builtins. Failures are
// recorded, not fatal — the frontend shows a setup-error banner via
// Health().
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if a.detachedClient != nil {
		a.detachedClient.start(ctx, func(name string, payload any) {
			wruntime.EventsEmit(ctx, name, payload)
		})
		// A detached child is ready as soon as its broker client is alive.
		// Do not make the parent wait for a heavy Studio renderer/import tree.
		go func() { _ = a.detachedClient.rpc("window.ready", nil, nil) }()
		return
	}

	// Add managed tool prefixes (graphify, gstack, scrapegraph) and the praimate bin dir
	// (praimate-code). Without it the GUI — and every CLI child it
	// spawns — can't resolve tools installed into the managed dirs:
	// "graphify installs but isn't detected".
	refreshManagedPaths()

	// Live PATH rescan — periodically check the well-known per-user CLI
	// dirs in case the user installed bun/pnpm/cargo/uv in another
	// terminal while the GUI was already running. Cheap: just os.Stat
	// per dir, no fork. Goroutine ends when ctx is canceled (i.e. on
	// shutdown). The interval is wide enough not to be a busy loop and
	// tight enough that "I just ran `curl … | bash`" feels immediate.
	go func() {
		t := time.NewTicker(8 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				refreshManagedPaths()
			}
		}
	}()

	dbPath, err := store.DefaultDBPath()
	if err != nil {
		a.initErr = err.Error()
		return
	}
	a.dbPath = dbPath
	st, err := store.Open(dbPath)
	if err != nil {
		if errors.Is(err, store.ErrPasswordRequired) ||
			errors.Is(err, store.ErrPasswordSetupRequired) {
			return
		}
		a.initErr = err.Error()
		return
	}
	if err := a.initializeUnlockedStore(ctx, st); err != nil {
		_ = st.Close()
		a.initErr = err.Error()
	}
}

func (a *App) initializeUnlockedStore(ctx context.Context, st *store.Store) error {
	// Load the legacy workspaces root (best-effort) so existing on-disk
	// chats remain available in the Code terminal. Without a config the
	// workspace chats section stays empty.
	workspacesRoot := ""
	var launcherCfg *launcher.Config
	if cfg, cfgErr := launcher.LoadConfig(); cfgErr == nil && cfg != nil {
		launcherCfg = cfg
		workspacesRoot = cfg.WorkspacesRoot
	}
	c, err := core.New(core.Options{Store: st, WorkspacesRoot: workspacesRoot})
	if err != nil {
		return err
	}
	if _, err := c.SeedBuiltins(ctx); err != nil {
		return err
	}
	if err := migrateLegacyLocalLLMAPIKey(c, launcherCfg); err != nil {
		return err
	}
	// Codex local routing is no longer supported. Remove only the
	// ollama_remote blocks previously owned by PrAImate; unrelated Codex
	// configuration is preserved by DisableCodex.
	_, _ = ollama.DisableCodex()
	core.RegisterAllCLIAdapters()
	// From here on, every backup commit snapshots the DB + shareable
	// config, and every pull/merge/reset row-merges the remote's
	// snapshot back in. Must precede any Backup-tab binding call.
	backup.SetStateSyncer(coreStateSyncer{core: c})
	// "Ask" Tools level: chats route mid-turn permission requests to
	// the GUI's Allow/Deny card (claude/openclaude only; see
	// approval_broker.go).
	c.SetApprovalProvider(a.approvalProvider)
	runGUIAutoSync(ctx, launcherCfg)

	if editorFolder == "" {
		// Watchers and schedules run in the main desktop process. NOT in studio-window
		// processes: the main window already runs them, and two daemons
		// on one DB would double-fire every schedule.
		watchers, _ := c.StartWatcherDaemon(context.Background(), core.WatcherDaemonOptions{
			WatcherDispatchOptions: core.WatcherDispatchOptions{CLI: "claude"},
		})
		schedules, _ := c.StartScheduleDaemon(context.Background(), core.ScheduleDaemonOptions{
			ScheduleDispatchOptions: core.ScheduleDispatchOptions{CLI: "claude"},
		})

		a.daemonMu.Lock()
		a.watchers = watchers
		a.schedules = schedules
		a.daemonMu.Unlock()
	}

	a.st = st
	a.core = c

	if editorFolder != "" {
		// Studio window: stream external file changes (the agent's
		// edits) into the open tabs.
		a.startEditorWatcher()
	}
	return nil
}

func (a *App) shutdown(ctx context.Context) {
	if a.detachedClient != nil {
		a.detachedClient.close()
		return
	}
	a.resetMu.Lock()
	resetting := a.dataReset
	a.resetMu.Unlock()
	a.daemonMu.Lock()
	if a.watchers != nil {
		a.watchers.Stop()
		a.watchers = nil
	}
	if a.schedules != nil {
		a.schedules.Stop()
		a.schedules = nil
	}
	a.daemonMu.Unlock()
	if a.terms != nil {
		a.terms.closeAll()
	}
	if !resetting {
		if cfg, err := launcher.LoadConfig(); err == nil {
			runGUIAutoSync(context.Background(), cfg)
		}
	}
	if a.st != nil {
		_ = a.st.Close()
	}
	if a.detached != nil {
		a.detached.close()
	}
}

// --- Terminal (live coding sessions) -------------------------------------

// PickProjectFolder opens a directory picker for a terminal's working
// folder.
func (a *App) PickProjectFolder() (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Choose project folder",
	})
}

// StartTerminal launches a third-party CLI live in a PTY for an agent. The
// existing project instructions are never overwritten. Persona and controlled
// pinned-skill context is temporary and exclusive. Returns the
// terminal session id; output streams over "term:data:<id>" events.
func (a *App) StartTerminal(agentID, cli, model, cwd, localEndpoint, _ string, localModel string, resume bool, skills []string) (string, error) {
	return a.startTerminal(agentID, cli, model, cwd, localEndpoint, localModel, resume, skills, nil)
}

func (a *App) startTerminal(agentID, cli, model, cwd, localEndpoint, localModel string, resume bool, skills []string, skillSettings *core.ChatSettings) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	if cwd == "" {
		return "", fmt.Errorf("a project folder is required")
	}
	var agent *core.Agent
	if agentID != "" {
		agent, err = c.GetAgent(a.ctx, agentID)
		if err != nil {
			return "", err
		}
	}
	var local *core.ChatLocalEndpoint
	if strings.TrimSpace(localEndpoint) != "" {
		local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
	}
	effective, err := c.ResolveExecutionConfig(a.ctx, core.ExecutionRequest{
		Surface: core.SurfaceTerminal, Agent: agent, CLI: cli, Cwd: cwd,
		Model: model, Local: local, AllEnabledMCP: agent == nil,
	})
	if err != nil {
		return "", err
	}
	model = effective.Model
	name, args, err := terminalCommand(cli, model)
	if err != nil {
		return "", err
	}
	if resume {
		var supported bool
		name, args, supported, err = terminalResumeCommand(cli, model, cwd)
		if err != nil {
			return "", err
		}
		// For CLIs without a safe non-interactive resume selector, retain the
		// normal launch rather than opening an interactive picker unexpectedly.
		_ = supported
	}
	// Interactive CLIs do not expose their final prompt. PrAImate therefore
	// uses an explicit compatible fallback: the exact reviewed pinned bodies go
	// into the CLI's temporary project context and resources into a private,
	// per-launch root. This never claims observable native activation.
	prefix := core.ResolveSkillsPrefix(skills)
	var nativePrefix string
	var nativeSkills *core.NativeSkillMaterialization
	var configured bool
	if skillSettings != nil {
		nativePrefix, nativeSkills, configured, err = c.PrepareTerminalSkillFallbackForSettings(a.ctx, *skillSettings)
	} else {
		nativePrefix, nativeSkills, configured, err = c.PrepareTerminalSkillFallback(a.ctx, agent)
	}
	if err != nil {
		return "", err
	}
	if configured {
		if len(skills) > 0 {
			_ = nativeSkills.Cleanup()
			return "", fmt.Errorf("legacy and versioned skill selections cannot be combined")
		}
		prefix = nativePrefix
	}
	legacyCleanup, err := prepareLegacyTerminalContext(cwd, cli, agent, prefix)
	if err != nil {
		if nativeSkills != nil {
			_ = nativeSkills.Cleanup()
		}
		return "", err
	}
	started := false
	defer func() {
		if !started {
			if legacyCleanup != nil {
				legacyCleanup()
			}
			if nativeSkills != nil {
				_ = nativeSkills.Cleanup()
			}
		}
	}()
	targetChatID := ""
	if agentID != "" {
		targetChatID = "agent:" + agentID
	}
	if prov := a.skillsProvider(targetChatID); prov != nil {
		effective.InternalMCPServers = append(effective.InternalMCPServers, core.MCPServer{
			ID:        "praimate_skills",
			Name:      "PrAImate Skills",
			Transport: core.MCPTransportStdio,
			Command:   prov.Command,
			Args:      prov.Args,
			Enabled:   true,
		})
	}
	// Validate the concrete terminal command before PrepareExecution writes
	// project-scoped MCP or provider configuration.
	if err := c.PrepareExecution(a.ctx, effective); err != nil {
		return "", err
	}
	env := appendEnvMap(nil, effective.Env)
	if nativeSkills != nil {
		env = appendEnvMap(env, nativeSkills.Env)
	}
	cleanup := func() {
		if legacyCleanup != nil {
			legacyCleanup()
		}
		if nativeSkills != nil {
			_ = nativeSkills.Cleanup()
		}
	}
	termID, err := a.terms.startWithCleanup(name, args, cwd, env, a.emitTerminalEvent, cleanup)
	if err != nil {
		cleanup()
	}
	started = err == nil
	return termID, err
}

// WriteTerminal forwards a base64-encoded chunk of keystrokes to the
// PTY. The frontend base64-encodes so arbitrary control bytes survive.
func (a *App) WriteTerminal(id, b64 string) error {
	if a.detachedClient != nil {
		return a.detachedClient.terminalWrite(id, b64)
	}
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("decode terminal input: %w", err)
	}
	return a.terms.write(id, data)
}

func (a *App) ResizeTerminal(id string, cols, rows int) error {
	if a.detachedClient != nil {
		return a.detachedClient.terminalResize(id, cols, rows)
	}
	return a.terms.resize(id, cols, rows)
}

// GetTerminalSnapshot returns the retained output of a live PTY so the Code
// page can reconstruct xterm after it was minimized/navigated away from.
func (a *App) GetTerminalSnapshot(id string) (TerminalSnapshot, error) {
	if a.detachedClient != nil {
		return a.detachedClient.terminalSnapshot(id)
	}
	return a.terms.snapshot(id)
}

func (a *App) CloseTerminal(id string) {
	if a.detachedClient != nil {
		_ = a.detachedClient.terminalClose(id)
		return
	}
	a.terms.close(id)
}

// InstallPraimateCodeResult tells the frontend whether the download
// succeeded, failed for a fixable network reason, or failed because no
// prebuilt asset exists for this OS/arch (in which case the GUI offers
// "Compile from source" instead of letting the user retry forever).
type InstallPraimateCodeResult struct {
	OK              bool   `json:"ok"`
	Log             string `json:"log"`
	Error           string `json:"error,omitempty"`
	NoPrebuiltAsset bool   `json:"noPrebuiltAsset,omitempty"`
}

// InstallPraimateCode downloads the prebuilt PrAImate Code binary into
// the managed bin dir. Returns a structured result so the frontend can
// distinguish "404 / asset missing" (offer compile) from a generic
// failure (offer retry).
func (a *App) InstallPraimateCode() InstallPraimateCodeResult {
	var buf strings.Builder
	err := installer.InstallPraimateCode(a.ctx, &buf)
	res := InstallPraimateCodeResult{OK: err == nil, Log: buf.String()}
	if err != nil {
		res.Error = err.Error()
		if errors.Is(err, installer.ErrNoPrebuiltAsset) {
			res.NoPrebuiltAsset = true
		}
	}
	return res
}

// PraimateCodeInstalled reports whether praimate-code resolves on this
// host (managed bin dir or PATH).
func (a *App) PraimateCodeInstalled() bool {
	bin, err := installer.PraimateBinDir()
	if err == nil {
		name := "praimate-code"
		if osIsWindows() {
			name += ".exe"
		}
		if fi, e := os.Stat(filepath.Join(bin, name)); e == nil && !fi.IsDir() {
			return true
		}
	}
	_, err = exec.LookPath("praimate-code")
	return err == nil
}

func osIsWindows() bool { return runtime.GOOS == "windows" }

func (a *App) requireCore() (*core.Core, error) {
	if a.core == nil {
		if a.initErr != "" {
			return nil, fmt.Errorf("core unavailable: %s", a.initErr)
		}
		return nil, fmt.Errorf("core not initialised yet")
	}
	return a.core, nil
}

// --- Health / meta -------------------------------------------------------

// Health reports init status so the frontend can render a setup-error
// banner instead of failing every call individually.
type Health struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	Version string `json:"version"`
	DBPath  string `json:"db_path,omitempty"`
}

func (a *App) Health() Health {
	h := Health{Version: version.Current}
	if a.core == nil {
		h.Error = a.initErr
		return h
	}
	h.OK = true
	h.DBPath = a.st.Path()
	return h
}

// --- Chats ---------------------------------------------------------------

func (a *App) ListChats() ([]core.Chat, error) {
	if a.detachedClient != nil {
		return a.detachedClient.listChats()
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	chats, err := c.ListChats(a.ctx, 200)
	if err != nil {
		return nil, err
	}
	return redactChatCredentials(chats), nil
}

// StartChat creates an interactive chat bound to an agent + CLI and
// returns it. The frontend then drives it with SendChat.
func (a *App) StartChat(agentID, cli, cwd string) (*core.Chat, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		} else {
			cwd = "."
		}
	}
	chat, err := c.StartInteractiveChat(a.ctx, agentID, cli, cwd)
	if err != nil {
		return nil, err
	}
	return redactChatCredential(chat), nil
}

// SendChat sends one message into an interactive chat and returns the
// assistant's reply. The agent's instructions frame the first turn of a
// fresh session automatically.
func (a *App) SendChat(chatID, message string) (*core.ChatTurn, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	chat, err := c.GetChat(a.ctx, chatID)
	if err != nil {
		return nil, err
	}
	systemPrompt := ""
	if chat.AgentID != "" {
		if agent, err := c.GetAgent(a.ctx, chat.AgentID); err == nil {
			systemPrompt = core.AgentSystemPrompt(agent)
		}
	}
	if prefix := core.ResolveSkillsPrefix(chat.Settings.Skills); prefix != "" {
		if systemPrompt != "" {
			systemPrompt = prefix + "\n\n---\n\n" + systemPrompt
		} else {
			systemPrompt = prefix
		}
	}
	return c.ContinueChat(a.ctx, chatID, message, chat.WorkspacePath, systemPrompt)
}

func (a *App) ChatMessages(chatID string) ([]core.Message, error) {
	if a.detachedClient != nil {
		return a.detachedClient.chatMessages(chatID)
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListMessages(a.ctx, chatID, 0)
}

func (a *App) DeleteChat(chatID string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := c.DeleteChat(a.ctx, chatID); err != nil {
		return err
	}
	return nil
}

// --- Clean chats (CLI + model, no agent) ----------------------------------

// CLIInfo describes one launchable CLI for the "new chat" picker.
type CLIInfo struct {
	ID        string   `json:"id"`
	Label     string   `json:"label"`
	Available bool     `json:"available"`
	ModelHint string   `json:"modelHint"` // expected --model format, "" = no model flag
	Models    []string `json:"models"`    // suggestions; free text always allowed
}

// modelHints documents each CLI's --model format. Empty string means
// the CLI has no model flag (the model input is disabled in the UI).
var modelHints = map[string]string{
	"claude":        "alias (sonnet, opus, haiku) or full model id",
	"openclaude":    "alias (sonnet, opus, haiku) or full model id",
	"codex":         "model id, e.g. gpt-5.1-codex",
	"opencode":      "provider/model, e.g. anthropic/claude-sonnet-4-5",
	"praimate-code": "provider/model, e.g. anthropic/claude-sonnet-4-5",
}

// staticModelSuggestions are fallback datalist entries for CLIs without
// a list command. They are SUGGESTIONS — the input stays free text, so
// new models work without a PrAImate release.
var staticModelSuggestions = map[string][]string{
	"claude":     {"sonnet", "opus", "haiku", "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"},
	"openclaude": {"sonnet", "opus", "haiku", "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"},
	"codex":      {"gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1", "o4-mini"},
}

// ListCLIs returns every launchable CLI with availability probed
// concurrently (a --version run each, bounded at 5s total).
func (a *App) ListCLIs() []CLIInfo {
	refreshManagedPaths()
	agents := launcher.KnownAgents()
	out := make([]CLIInfo, len(agents))
	// 5s was too tight: a slow `opencode --version` (Bun cold start)
	// or a stalled `codex --version` would race past the deadline and
	// the CLI got rendered as "not installed" in the chat/agent
	// selector even though the CLIs tab (30s budget) showed it
	// installed. 20s leaves headroom for the slowest healthy probe.
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, ag := range agents {
		out[i] = CLIInfo{
			ID:        string(ag.ID),
			Label:     ag.Label,
			ModelHint: modelHints[string(ag.ID)],
			Models:    staticModelSuggestions[string(ag.ID)],
		}
		adapter, err := core.GetCLIAdapter(string(ag.ID))
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(i int, ad core.CLIAdapter) {
			defer wg.Done()
			out[i].Available = ad.Available(ctx) == nil
		}(i, adapter)
	}
	wg.Wait()
	return out
}

// ListCLIModels returns live model suggestions when the CLI exposes a
// catalogue; otherwise it falls back to the static suggestions.
func (a *App) ListCLIModels(cli string) []string {
	seen := map[string]bool{}
	var models []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}

	if cli == "openclaude" {
		// Include OpenClaude configured local profile model if present
		if profile, ok := launcher.ReadOpenClaudeLocalProfile(); ok && profile.Model != "" {
			add(profile.Model)
		}
		// Include static aliases and models
		for _, m := range staticModelSuggestions["openclaude"] {
			add(m)
		}
		return models
	}

	if cli == "codex" {
		live := a.listCodexModels()
		for _, m := range live {
			add(m)
		}
		for _, m := range staticModelSuggestions["codex"] {
			add(m)
		}
		return models
	}

	if cli == "opencode" || cli == "praimate-code" {
		// Include models configured in opencode.json
		if ocModels, err := ollama.ListConfiguredOpenCodeModels(); err == nil {
			for pKey, mList := range ocModels {
				for _, m := range mList {
					add(pKey + "/" + m)
					add(m)
				}
			}
		}
		// Probe opencode CLI models if available
		if bin, err := exec.LookPath(cli); err == nil {
			ctx, cancel := context.WithTimeout(a.ctx, 8*time.Second)
			cmd := exec.CommandContext(ctx, bin, "models")
			hideConsole(cmd)
			if outBytes, err := cmd.Output(); err == nil {
				for _, line := range strings.Split(string(outBytes), "\n") {
					line = strings.TrimSpace(line)
					if line != "" && strings.Contains(line, "/") && !strings.Contains(line, " ") {
						add(line)
					}
				}
			}
			cancel()
		}
		// Fallback defaults
		for _, m := range []string{"anthropic/claude-sonnet-4-5", "openai/gpt-5", "anthropic/claude-opus-4-6", "anthropic/claude-haiku-4-5"} {
			add(m)
		}
		return models
	}

	for _, m := range staticModelSuggestions[cli] {
		add(m)
	}
	return models
}

func (a *App) listCodexModels() []string {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "models")
	hideConsole(cmd)
	outBytes, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseCodexDebugModels(outBytes)
}

func parseCodexDebugModels(raw []byte) []string {
	var payload struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var models []string
	for _, item := range payload.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" || seen[slug] || item.Visibility != "list" {
			continue
		}
		seen[slug] = true
		models = append(models, slug)
	}
	return models
}

// StartCleanChat creates an agent-less chat on a CLI with an optional
// pinned model. The frontend drives it with SendChat like any other
// chat; SendChat finds no AgentID and sends no system prompt.
func (a *App) StartCleanChat(cli, model, cwd string) (*core.Chat, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	if cwd == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cwd = home
		} else {
			cwd = "."
		}
	}
	chat, err := c.StartCleanChat(a.ctx, cli, model, cwd)
	if err != nil {
		return nil, err
	}
	return redactChatCredential(chat), nil
}

// --- Legacy workspace chats ------------------------------------------------

// WorkspaceChatInfo is one legacy on-disk chat that the GUI can reopen
// in the Code terminal. Its workpath and sandbox remain on disk.
type WorkspaceChatInfo struct {
	ID       string    `json:"id"`
	Label    string    `json:"label"`
	Agent    string    `json:"agent"`
	Template string    `json:"template"`
	LastUsed time.Time `json:"lastUsed"`
	Sandbox  string    `json:"sandbox"`
}

// ListWorkspaceChats lists legacy on-disk chats (newest first).
// It returns an empty list when no workspaces root is configured.
func (a *App) ListWorkspaceChats() ([]WorkspaceChatInfo, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	chats, err := c.ListLegacyChats(a.ctx)
	if err != nil {
		return []WorkspaceChatInfo{}, nil // unconfigured root = empty, not an error
	}
	out := make([]WorkspaceChatInfo, 0, len(chats))
	for _, ch := range chats {
		out = append(out, WorkspaceChatInfo{
			ID: ch.ID, Label: ch.Label, Agent: string(ch.AgentID),
			Template: ch.Template, LastUsed: ch.LastUsed, Sandbox: ch.SandboxDir,
		})
	}
	return out, nil
}

// OpenWorkspaceChatResult is what OpenWorkspaceChat hands the frontend
// so the Code page can attach to the already-running terminal.
type OpenWorkspaceChatResult struct {
	TermID string `json:"termId"`
	CLI    string `json:"cli"`
	Cwd    string `json:"cwd"`
	Label  string `json:"label"`
	Note   string `json:"note"`
}

// OpenWorkspaceChat relaunches a legacy chat's CLI inside the GUI's Code
// terminal: same sandbox, same agent CLI, native session resume where
// the CLI supports it (claude/openclaude/codex).
func (a *App) OpenWorkspaceChat(chatID string) (*OpenWorkspaceChatResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	root := c.WorkspacesRoot()
	if root == "" {
		return nil, fmt.Errorf("no workspaces root configured; complete first-run setup")
	}
	chat, err := launcher.LoadChat(root, chatID)
	if err != nil || chat == nil {
		return nil, fmt.Errorf("load workspace chat %q: %w", chatID, err)
	}
	var agent *launcher.Agent
	for _, ag := range launcher.KnownAgents() {
		if ag.ID == chat.AgentID {
			ag := ag
			agent = &ag
			break
		}
	}
	if agent == nil {
		return nil, fmt.Errorf("unknown agent %q on chat %q", chat.AgentID, chatID)
	}
	name, args, err := terminalCommand(string(chat.AgentID), "")
	if err != nil {
		return nil, err
	}
	resume := launcher.RestoreNativeSession(*agent, *chat)
	args = append(args, resume.Args...)
	termID, err := a.terms.start(name, args, chat.SandboxDir, nil, a.emitTerminalEvent)
	if err != nil {
		return nil, err
	}
	if err := a.terms.bindChat(termID, chatID); err != nil {
		a.terms.close(termID)
		return nil, err
	}
	return &OpenWorkspaceChatResult{
		TermID: termID,
		CLI:    string(chat.AgentID),
		Cwd:    chat.SandboxDir,
		Label:  chat.Label,
		Note:   resume.Note,
	}, nil
}

// ListTerminalSessions returns the live PTYs the GUI is managing. The
// Sessions panel uses this to offer "resume" for terminal chats whose
// PTY is still alive (instead of starting a duplicate).
func (a *App) ListTerminalSessions() []TermInfo {
	if a.terms == nil {
		return nil
	}
	return a.terms.list()
}

// --- Agents ----------------------------------------------------------------

// hiddenAgentIDs are built-in agents that exist in the DB (so the studio
// helper / installer pipelines can use them) but should NOT show up in
// the GUI's Agents list — embedding-only agents the user shouldn't open.
var hiddenAgentIDs = map[string]bool{
	"agent-builder": true, // drives the New-Agent studio's authoring assistant
}

func (a *App) ListAgents() ([]core.Agent, error) {
	if a.detachedClient != nil {
		return a.detachedClient.listAgents()
	}
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	all, err := c.ListAgents(a.ctx)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, ag := range all {
		if hiddenAgentIDs[ag.ID] {
			continue
		}
		out = append(out, ag)
	}
	return out, nil
}

// ImportAgentDialog opens a native file picker and imports the chosen
// agent — bare YAML or a .praimate-agent knowledge pack. Returns the
// imported agent, or nil if the user cancelled.
func (a *App) ImportAgentDialog() (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Import agent (YAML or pack)",
		Filters: []wruntime.FileFilter{
			{DisplayName: "Agents", Pattern: "*.yaml;*.yml;*" + core.AgentPackExt + ";*.zip"},
		},
	})
	if err != nil || path == "" {
		return nil, err
	}
	return c.ImportAgentAuto(a.ctx, path)
}

// ReviewAgentImportDialog returns an immutable pack inventory for one explicit
// host review. Bare YAML remains a compatibility import and carries no bundled
// code or trust decision.
func (a *App) ReviewAgentImportDialog() (*core.AgentPackReview, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	path, err := wruntime.OpenFileDialog(a.ctx, wruntime.OpenDialogOptions{
		Title:   "Import agent",
		Filters: []wruntime.FileFilter{{DisplayName: "Agents", Pattern: "*.yaml;*.yml;*" + core.AgentPackExt + ";*.zip"}},
	})
	if err != nil || path == "" {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case core.AgentPackExt, ".zip":
		return c.InspectAgentPack(a.ctx, path)
	default:
		agent, err := c.ImportAgent(a.ctx, path)
		if err != nil {
			return nil, err
		}
		return &core.AgentPackReview{Agent: agent}, nil
	}
}

func (a *App) ImportReviewedAgentPack(path, reviewDigest string) (*core.Agent, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ImportReviewedAgentPack(a.ctx, path, reviewDigest)
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// ExportAgentDialog opens a native save dialog and writes the agent
// YAML there. Returns the chosen path ("" if cancelled).
func (a *App) ExportAgentDialog(agentID string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	path, err := wruntime.SaveFileDialog(a.ctx, wruntime.SaveDialogOptions{
		Title:           "Export agent YAML",
		DefaultFilename: agentID + ".yaml",
	})
	if err != nil || path == "" {
		return "", err
	}
	if err := c.ExportAgent(a.ctx, agentID, path); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) DeleteAgent(agentID string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.DeleteAgent(a.ctx, agentID)
}

// --- Run (workflow execution) ---------------------------------------------

// PickFolder opens a directory picker for the run cwd.
func (a *App) PickFolder() (string, error) {
	return wruntime.OpenDirectoryDialog(a.ctx, wruntime.OpenDialogOptions{
		Title: "Choose working folder",
	})
}

// TurnEvent is the payload emitted on the "praimate:turn" event while
// a workflow run is in flight.
type TurnEvent struct {
	Index        int    `json:"index"`
	WorkflowName string `json:"workflow_name,omitempty"`
	UserMsg      string `json:"user_msg"`
	Reply        string `json:"reply"`
	DurationMs   int64  `json:"duration_ms"`
}

// WorkflowStreamEvent is emitted on "praimate:workflow-stream" while a
// workflow run is active.
type WorkflowStreamEvent struct {
	WorkflowName string `json:"workflow_name"`
	TurnIndex    int    `json:"turn_index"`
	Type         string `json:"type"`
	Text         string `json:"text,omitempty"`
	Tool         string `json:"tool,omitempty"`
	Detail       string `json:"detail,omitempty"`
	ID           string `json:"id,omitempty"`
	OK           bool   `json:"ok,omitempty"`
}

// RunResult mirrors core.RunResult in a JSON-friendly shape.
type RunResult struct {
	Outcome   string      `json:"outcome"`
	Error     string      `json:"error,omitempty"`
	ChatID    string      `json:"chat_id,omitempty"`
	Turns     []TurnEvent `json:"turns"`
	SessionID string      `json:"session_id,omitempty"`
}

// RunWorkflow executes one agent workflow synchronously (the JS
// promise resolves when the run completes); per-turn progress streams
// via the "praimate:turn" event.
func (a *App) RunWorkflow(agentID, workflowName, cli, model, cwd string, inputs map[string]string, localEndpoint, _ string, localModel string) (*RunResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, agentID)
	if err != nil {
		return nil, err
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil, fmt.Errorf("a working folder is required")
	}
	settings := workflowChatSettings(model, localEndpoint, localModel)

	res := c.RunWorkflow(a.ctx, core.RunOptions{
		Agent:        agent,
		WorkflowName: workflowName,
		Inputs:       inputs,
		CLI:          cli,
		Cwd:          cwd,
		Model:        model,
		Tools:        settings.Tools,
		Persist:      true,
		ChatTitle:    agent.Name + " · " + workflowName,
		ChatSettings: settings,
		OnTurn: func(t core.TurnResult) {
			wruntime.EventsEmit(a.ctx, "praimate:turn", TurnEvent{
				Index:        t.Index,
				WorkflowName: t.WorkflowName,
				UserMsg:      t.UserMsg,
				Reply:        replyText(t.Reply),
				DurationMs:   t.DurationMs,
			})
		},
		OnEvent: func(ev core.WorkflowRunEvent) {
			wruntime.EventsEmit(a.ctx, "praimate:workflow-stream", WorkflowStreamEvent{
				WorkflowName: ev.WorkflowName,
				TurnIndex:    ev.TurnIndex,
				Type:         ev.Type,
				Text:         ev.Text,
				Tool:         ev.Tool,
				Detail:       ev.Detail,
				ID:           ev.ID,
				OK:           ev.OK,
			})
		},
	})

	out := guiRunResult(res)
	return out, nil
}

// RunAllWorkflows executes every workflow on the agent in declaration
// order, sharing one resumable CLI session across the sequence.
func (a *App) RunAllWorkflows(agentID, cli, model, cwd string, inputsByWorkflow map[string]map[string]string, localEndpoint, _ string, localModel string) (*RunResult, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	agent, err := c.GetAgent(a.ctx, agentID)
	if err != nil {
		return nil, err
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil, fmt.Errorf("a working folder is required")
	}
	settings := workflowChatSettings(model, localEndpoint, localModel)

	res := c.RunAllWorkflows(a.ctx, core.RunAllOptions{
		Agent:            agent,
		InputsByWorkflow: inputsByWorkflow,
		CLI:              cli,
		Cwd:              cwd,
		Model:            model,
		Tools:            settings.Tools,
		Persist:          true,
		ChatTitle:        agent.Name + " · all workflows",
		ChatSettings:     settings,
		OnTurn: func(t core.TurnResult) {
			wruntime.EventsEmit(a.ctx, "praimate:turn", TurnEvent{
				Index:        t.Index,
				WorkflowName: t.WorkflowName,
				UserMsg:      t.UserMsg,
				Reply:        replyText(t.Reply),
				DurationMs:   t.DurationMs,
			})
		},
		OnEvent: func(ev core.WorkflowRunEvent) {
			wruntime.EventsEmit(a.ctx, "praimate:workflow-stream", WorkflowStreamEvent{
				WorkflowName: ev.WorkflowName,
				TurnIndex:    ev.TurnIndex,
				Type:         ev.Type,
				Text:         ev.Text,
				Tool:         ev.Tool,
				Detail:       ev.Detail,
				ID:           ev.ID,
				OK:           ev.OK,
			})
		},
	})
	out := guiRunResult(res)
	return out, nil
}

func workflowChatSettings(model, localEndpoint, localModel string) core.ChatSettings {
	settings := core.ChatSettings{Surface: "workflow", Model: model}
	if strings.TrimSpace(localEndpoint) != "" {
		settings.Local = &core.ChatLocalEndpoint{Endpoint: localEndpoint, Model: localModel}
	}
	return settings
}

func guiRunResult(res *core.RunResult) *RunResult {
	out := &RunResult{
		Outcome:   string(res.Outcome),
		ChatID:    res.ChatID,
		SessionID: res.SessionID,
	}
	if res.Err != nil {
		out.Error = res.Err.Error()
	}
	for _, t := range res.Turns {
		out.Turns = append(out.Turns, TurnEvent{
			Index:        t.Index,
			WorkflowName: t.WorkflowName,
			UserMsg:      t.UserMsg,
			Reply:        replyText(t.Reply),
			DurationMs:   t.DurationMs,
		})
	}
	return out
}

func replyText(reply *core.Reply) string {
	if reply == nil {
		return ""
	}
	return reply.Text
}

// PrivacyPreview scans the would-be outbound text and returns category
// counts so the frontend can render a review sheet without ever
// echoing the secret values.
func (a *App) PrivacyPreview(text string) (map[string]int, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, m := range c.PrivacyScanner().Match(text) {
		counts[string(m.Category)]++
	}
	return counts, nil
}

// --- MCP ---------------------------------------------------------------------

func (a *App) MCPCatalogue() []core.MCPCatalogueEntry {
	entries := core.ListMCPCatalogue()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (a *App) MCPServers() ([]core.MCPServer, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListMCPServers(a.ctx, false)
}

// AddCustomMCP registers a user-defined MCP server (local or remote)
// that isn't in the catalogue — e.g. hexstrike-ai. envText is
// newline/comma-separated KEY=VALUE pairs.
func (a *App) AddCustomMCP(name, transport, command, url, envText string) (*core.MCPServer, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.AddCustomMCP(a.ctx, core.AddCustomMCPRequest{
		Name:      name,
		Transport: transport,
		Command:   command,
		URL:       url,
		Env:       core.ParseEnvLines(envText),
	})
}

// UpdateMCPServer edits an MCP server while preserving its stable ID and
// enabled state.
func (a *App) UpdateMCPServer(id, name, transport, command, url, envText string) (*core.MCPServer, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.UpdateMCPServer(a.ctx, id, core.AddCustomMCPRequest{
		Name:      name,
		Transport: transport,
		Command:   command,
		URL:       url,
		Env:       core.ParseEnvLines(envText),
	})
}

func (a *App) SetMCPEnabled(id string, enabled bool) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.SetMCPEnabled(a.ctx, id, enabled)
}

func (a *App) DeleteMCPServer(id string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.DeleteMCPServer(a.ctx, id)
}

// --- Automation (watchers + schedules) --------------------------------------

func (a *App) ListWatchers() ([]core.Watcher, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListWatchers(a.ctx, false)
}

func (a *App) AddWatcher(agentID, path, workflow string, patterns []string) (int64, error) {
	c, err := a.requireCore()
	if err != nil {
		return 0, err
	}
	id, err := c.AddWatcher(a.ctx, core.AddWatcherRequest{
		AgentID: agentID, Path: path, Workflow: workflow, Patterns: patterns,
	})
	if err == nil {
		a.restartWatchers()
	}
	return id, err
}

func (a *App) SetWatcherEnabled(id int64, enabled bool) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := c.SetWatcherEnabled(a.ctx, id, enabled); err != nil {
		return err
	}
	a.restartWatchers()
	return nil
}

func (a *App) DeleteWatcher(id int64) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := c.DeleteWatcher(a.ctx, id); err != nil {
		return err
	}
	a.restartWatchers()
	return nil
}

// restartWatchers rebuilds the fsnotify daemon after watcher rows
// change so newly authored paths are observed immediately.
func (a *App) restartWatchers() {
	if a.core == nil {
		return
	}
	a.daemonMu.Lock()
	defer a.daemonMu.Unlock()
	if a.watchers != nil {
		a.watchers.Stop()
		a.watchers = nil
	}
	a.watchers, _ = a.core.StartWatcherDaemon(context.Background(), core.WatcherDaemonOptions{
		WatcherDispatchOptions: core.WatcherDispatchOptions{CLI: "claude"},
	})
}

func (a *App) ListSchedules() ([]core.Schedule, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListSchedules(a.ctx, false)
}

func (a *App) AddCronSchedule(agentID, cron, workflow string) (int64, error) {
	c, err := a.requireCore()
	if err != nil {
		return 0, err
	}
	return c.AddSchedule(a.ctx, core.AddScheduleRequest{
		AgentID: agentID, Cron: cron, Workflow: workflow,
	})
}

func (a *App) SetScheduleEnabled(id int64, enabled bool) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.SetScheduleEnabled(a.ctx, id, enabled)
}

func (a *App) DeleteSchedule(id int64) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.DeleteSchedule(a.ctx, id)
}

// --- Privacy patterns --------------------------------------------------------

func (a *App) ListPrivacyPatterns() ([]string, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	return c.ListPrivacyPatterns(a.ctx)
}

func (a *App) AddPrivacyPattern(pattern string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.AddPrivacyPattern(a.ctx, pattern)
}

func (a *App) DeletePrivacyPattern(index int) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.DeletePrivacyPattern(a.ctx, index)
}

// --- GUI settings -----------------------------------------------------------

func (a *App) GetGUISetting(key string) (string, error) {
	c, err := a.requireCore()
	if err != nil {
		return "", err
	}
	raw, err := c.GetSetting(a.ctx, core.ScopeGUI, key)
	if err != nil || raw == nil {
		return "", err
	}
	return string(raw), nil
}

func (a *App) SetGUISetting(key, valueJSON string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.SetSetting(a.ctx, core.ScopeGUI, key, []byte(valueJSON))
}

// RenameChat changes the title of an existing chat.
func (a *App) RenameChat(chatID, newTitle string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.RenameChat(a.ctx, chatID, newTitle)
}
