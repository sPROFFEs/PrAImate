package launcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/installer"
)

// ErrProbeTimeout is returned by probeVersion when the binary exists
// and runs, but takes longer than the deadline to print --version
// output. DetectAgents treats this case as "available, version unknown"
// rather than "broken install" — slow Node CLIs on Windows can take
// 10+s for a cold start.
var ErrProbeTimeout = errors.New("--version probe timed out")

// AgentID identifies a supported CLI agent.
type AgentID string

const (
	AgentClaude      AgentID = "claude"
	AgentOpenClaude  AgentID = "openclaude"
	AgentCodex       AgentID = "codex"
	AgentCopilot     AgentID = "copilot"
	AgentAntigravity AgentID = "antigravity"
	AgentOpenCode    AgentID = "opencode"
	// Retained only to read legacy persisted settings; neither ID is in
	// KnownAgents and neither can be installed or selected.
	AgentGemini       AgentID = "gemini"
	AgentDeepSeek     AgentID = "deepseek"
	AgentPraimateCode AgentID = "praimate-code"
	AgentPraimateCLI  AgentID = "praimate-cli"
)

// Agent describes one supported CLI agent. WpcTarget is the wpc target
// the launcher uses to compile the workpath into the sandbox before
// launching this agent.
type Agent struct {
	ID        AgentID
	Label     string
	Binary    string
	WpcTarget string // "claude" or "codex"
	// Available is true when the binary is on PATH and either `--version`
	// exits cleanly OR the version probe times out. A real probe failure
	// still means the install is broken, but a slow cold-starting Node CLI
	// should remain launchable.
	Available bool
	Version   string
	// ProbeError, when set, is the reason --version failed even though the
	// binary was on PATH. Surfaced in the picker so the user understands
	// why a "found but broken" install is greyed out.
	ProbeError string
	// InstallHint is a single command the user can run to install the
	// agent themselves. Surfaced in the picker for greyed-out entries.
	InstallHint string
}

// KnownAgents returns the static catalog. Availability is filled by
// DetectAgents.
func KnownAgents() []Agent {
	return []Agent{
		{
			ID:          AgentClaude,
			Label:       "Claude Code",
			Binary:      "claude",
			WpcTarget:   "claude",
			InstallHint: "curl -fsSL https://claude.ai/install.sh | bash   (Linux)  |  winget install Anthropic.ClaudeCode  (Windows)",
		},
		{
			// OpenClaude is a Claude Code fork that routes through any
			// OpenAI-compatible endpoint (Ollama, GPUStack, GitHub
			// Models, OAuth'd Codex, etc.) via the CLAUDE_CODE_USE_OPENAI
			// env switch. CLI surface, session-store layout
			// (~/.openclaude/projects/<slug>/<uuid>.jsonl), --continue /
			// --resume / --session-id flags, positional prompt arg,
			// CLAUDE.md auto-discovery — all inherited from upstream.
			// We compile via the same "claude" wpc target so SKILL.md +
			// CLAUDE.md scaffolding lands where openclaude already looks.
			ID:        AgentOpenClaude,
			Label:     "OpenClaude",
			Binary:    "openclaude",
			WpcTarget: "claude",
			// pnpm-only on purpose — npm has been the vector for the
			// recent supply-chain attacks (chalk/debug Sept 2025,
			// lottiefiles, etc.). pnpm + explicit registry pinning in
			// the installer narrows the trust surface.
			InstallHint: "npm install -g --no-fund --no-audit --ignore-scripts @gitlawb/openclaude",
		},
		{
			ID:          AgentCodex,
			Label:       "Codex CLI",
			Binary:      "codex",
			WpcTarget:   "codex",
			InstallHint: "npm install -g --no-fund --no-audit @openai/codex   (needs Node)",
		},
		{
			ID:          AgentOpenCode,
			Label:       "OpenCode",
			Binary:      "opencode",
			WpcTarget:   "codex",
			InstallHint: "curl -fsSL https://opencode.ai/install | bash   |  npm install -g --no-fund --no-audit opencode-ai",
		},
		{
			ID:     AgentPraimateCode,
			Label:  "PrAImate Code (bundled OpenCode build)",
			Binary: "praimate-code",
			// OpenCode-based: same AGENTS.md context convention as the
			// codex/opencode wpc target, so the mission compiles to a
			// file praimate-code picks up at launch.
			WpcTarget:   "codex",
			InstallHint: "install from the CLIs tab, or: praimate -install-tool... (downloads the bundled build)",
		},
		{
			ID:          AgentPraimateCLI,
			Label:       "PrAImate CLI (Native Go Agent)",
			Binary:      "praimate-cli",
			WpcTarget:   "codex",
			InstallHint: "bundled native Go CLI agent (0 external runtime dependencies)",
		},
		{ID: AgentCopilot, Label: "GitHub Copilot CLI", Binary: "copilot", WpcTarget: "codex", InstallHint: "npm install -g @github/copilot"},
		{ID: AgentAntigravity, Label: "Antigravity CLI", Binary: "agy", WpcTarget: "codex", InstallHint: "curl -fsSL https://antigravity.google/cli/install.sh | bash  |  Windows: irm https://antigravity.google/cli/install.ps1 | iex"},
	}
}

// DetectAgents probes a prioritized list of candidate paths for each
// agent and picks the first one whose `--version` actually exits 0.
// The order is:
//
//  1. exec.LookPath (whatever's on PATH first)
//  2. known per-agent install dirs (~/.opencode/bin, ~/.claude/local, ...)
//
// Trying every candidate matters because a broken binary can be on PATH
// while a working one sits in a known install dir — the real user case:
// pnpm's opencode.ps1 shim on %PATH% wraps a Windows-incompatible binary,
// but the official curl installer dropped a working native binary at
// ~/.opencode/bin/opencode.exe. We pick the second one.
//
// On success, Agent.Binary is rewritten to the absolute path of the
// resolved binary so the launcher can exec it directly even if the dir
// isn't on PATH in our process.
func DetectAgents(ctx context.Context) []Agent {
	agents := KnownAgents()
	var wg sync.WaitGroup
	for i := range agents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agents[i] = detectAgent(ctx, agents[i])
		}(i)
	}
	wg.Wait()
	return agents
}

func detectAgent(ctx context.Context, agent Agent) Agent {
	candidates := candidatePaths(agent.ID, agent.Binary)
	var lastErr error
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err != nil || st.IsDir() {
			continue
		}
		version, perr := probeVersion(ctx, candidate)
		if perr != nil {
			if errors.Is(perr, ErrProbeTimeout) {
				agent.Available = true
				agent.Binary = candidate
				break
			}
			if lastErr == nil {
				lastErr = perr
			}
			continue
		}
		agent.Available = true
		agent.Version = version
		agent.Binary = candidate
		break
	}
	if !agent.Available && lastErr != nil {
		agent.ProbeError = trimErr(lastErr)
	}
	return agent
}

// candidatePaths returns the ordered list of full paths to try when
// detecting an agent: PATH first, then known install dirs as fallback.
func candidatePaths(id AgentID, binary string) []string {
	var paths []string
	if p, err := exec.LookPath(binary); err == nil {
		paths = append(paths, p)
	}
	for _, p := range knownInstallPaths(id, binary) {
		paths = append(paths, p)
	}
	return paths
}

// knownInstallPaths returns common per-agent locations to probe when
// exec.LookPath returns nothing. The official install scripts for these
// agents update the user shell rc; a Windows-native PrAImate
// process never sees that PATH change.
func knownInstallPaths(id AgentID, binary string) []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	bins := []string{binary}
	if runtime.GOOS == "windows" {
		bins = append(bins, binary+".exe", binary+".cmd", binary+".bat")
	}

	var dirs []string
	switch id {
	case AgentOpenCode:
		// opencode.ai/install drops the binary here on every OS.
		dirs = append(dirs, filepath.Join(home, ".opencode", "bin"))
	case AgentClaude:
		dirs = append(dirs,
			filepath.Join(home, ".claude", "local"),
			filepath.Join(home, ".local", "bin"),
		)
	case AgentAntigravity:
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
		if runtime.GOOS == "windows" {
			dirs = append(dirs, filepath.Join(os.Getenv("LOCALAPPDATA"), "agy", "bin"))
		}
	case AgentCodex, AgentCopilot:
		if id == AgentCopilot {
			dirs = append(dirs, CopilotNativeBinDirs()...)
		}
		// npm/pnpm globals — covered by ImportPnpmPathIfPresent at
		// startup, but keep an explicit fallback for npm's location.
		if runtime.GOOS == "windows" {
			if appdata := os.Getenv("APPDATA"); appdata != "" {
				dirs = append(dirs, filepath.Join(appdata, "npm"))
			}
		}
	case AgentPraimateCode, AgentPraimateCLI:
		// InstallPraimateCode drops the binary into <config>/praimate/bin.
		// ImportPraimateBinToPath normally puts that dir on PATH, but probe
		// it explicitly so detection works even when the PATH import hasn't
		// run in this process (fresh GUI start, tests, `praimate code`).
		if binDir, err := installer.PraimateBinDir(); err == nil {
			dirs = append(dirs, binDir)
		}
		if exe, err := os.Executable(); err == nil {
			dirs = append(dirs, filepath.Dir(exe))
		}
	case AgentOpenClaude:
		// OpenClaude installs into a PrAImate-managed prefix (hoisted
		// node-linker) to dodge its phantom @aws-sdk dependency — its
		// bin lives at <prefix>/node_modules/.bin/openclaude, NOT on
		// the global pnpm path. Probe there first.
		if binDir, err := installer.ManagedAgentBinDir("openclaude"); err == nil {
			dirs = append(dirs, binDir)
		}
		// Legacy clade-managed prefix (pre-rebrand installs).
		if base, err := os.UserConfigDir(); err == nil {
			dirs = append(dirs, filepath.Join(base, "clade", "agents", "openclaude", "node_modules", ".bin"))
		}
		// Fallback: a stray global install (pnpm or npm) from a user
		// who installed by hand. ImportPnpmPathIfPresent covers the
		// pnpm-global PATH case; mirror codex's Windows-npm fallback.
		if runtime.GOOS == "windows" {
			if appdata := os.Getenv("APPDATA"); appdata != "" {
				dirs = append(dirs, filepath.Join(appdata, "npm"))
			}
		}
	}

	var paths []string
	for _, d := range dirs {
		for _, b := range bins {
			paths = append(paths, filepath.Join(d, b))
		}
	}
	return paths
}

// npm installs a small JS launcher and a platform package. On Windows prefer
// the native binary so headless launches do not depend on cmd.exe quoting.
func CopilotNativeBinDirs() []string {
	if runtime.GOOS != "windows" {
		return nil
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	var roots []string
	if shim, err := exec.LookPath("copilot"); err == nil {
		roots = append(roots, filepath.Dir(shim))
	}
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		roots = append(roots, filepath.Join(appdata, "npm"))
	}
	var dirs []string
	for _, root := range roots {
		pkg := "copilot-win32-" + arch
		dirs = append(dirs, filepath.Join(root, "node_modules", "@github", pkg), filepath.Join(root, "node_modules", "@github", "copilot", "node_modules", "@github", pkg))
	}
	return dirs
}

// probeVersion runs `<bin> --version` with a generous timeout. Returns
// the version line on clean exit, ErrProbeTimeout on timeout, and any
// other non-nil error for a real broken install.
//
// Timeout note: bumped to 8s. The previous 3s killed Node-based agents
// on Windows (opencode, codex, deepseek-tui) where the first --version
// invocation per shell takes 3-6s due to Node startup + first-run
// telemetry/cache priming. The verdict became "broken install" on a
// perfectly working binary. 8s leaves headroom for cold-start while
// still failing fast on hung binaries.
//
// When the deadline IS hit, the error string is reshaped from the raw
// "signal: killed" (which reads as "the binary crashed") to an
// explicit "--version timed out after 8s" so the install screen
// surfaces actionable text.
func probeVersion(parent context.Context, path string) (string, error) {
	const deadline = 8 * time.Second
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	probe := exec.CommandContext(ctx, path, "--version")
	hideConsole(probe)
	out, err := probe.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%w after %s (binary is slow to start, "+
				"not necessarily broken — try invoking it directly to confirm)", ErrProbeTimeout, deadline)
		}
		if installer.IsIllegalInstruction(err) {
			// Bun AVX2 build on a non-AVX2 CPU — the raw "exit status
			// 0xc000001d" reads like a corrupt download; say what it means.
			return "", fmt.Errorf("%w (illegal instruction — CPU lacks AVX2; reinstall to get the baseline build)", err)
		}
		// Surface what the binary printed before dying (Bun crash
		// reports, Node stack traces) — "exit status 3" alone hides it.
		if line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]); line != "" {
			if len(line) > 160 {
				line = line[:157] + "..."
			}
			return "", fmt.Errorf("%w — %s", err, line)
		}
		return "", err
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line), nil
}

// trimErr keeps Windows error blobs short enough to render in the picker.
func trimErr(err error) string {
	s := err.Error()
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}
