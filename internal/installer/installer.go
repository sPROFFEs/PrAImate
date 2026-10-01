// Package installer ports the agent-cli-installer.sh logic to Go so the
// launcher can install / update agent CLIs without shelling out to Bash
// (which isn't installed by default on Windows). Every method is a single
// shell-style command line; we expose them to the TUI as a sorted catalog
// with prerequisite checks.
package installer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
)

// OS is the launcher's runtime classification. Distinct from runtime.GOOS
// because we treat WSL as different from native Linux (some install
// scripts gate on it).
type OS string

const (
	OSMacOS   OS = "macos"
	OSLinux   OS = "linux"
	OSWSL     OS = "wsl"
	OSWindows OS = "windows"
)

// DetectOS returns the launcher's OS classification. Cheap; safe to call
// once per Update.
func DetectOS() OS {
	switch runtime.GOOS {
	case "darwin":
		return OSMacOS
	case "windows":
		return OSWindows
	case "linux":
		if isWSL() {
			return OSWSL
		}
		return OSLinux
	default:
		return OS(runtime.GOOS)
	}
}

func isWSL() bool {
	for _, p := range []string{"/proc/version", "/proc/sys/kernel/osrelease"} {
		raw, err := os.ReadFile(p)
		if err == nil && strings.Contains(strings.ToLower(string(raw)), "microsoft") {
			return true
		}
	}
	return false
}

// Method is one way to install or update an agent. It carries enough
// context for the TUI to show "we're about to run: <cmd>" before doing
// anything.
type Method struct {
	ID          string   // "curl", "pnpm", "brew", "winget", "powershell"
	Label       string   // human-readable line shown in the picker
	Command     string   // single shell-style command line
	Shell       Shell    // how to invoke Command
	Recommended bool     // exactly one method per (agent,action,os) is the default
	Prereqs     []string // names probed before running ("node", "pnpm")

	// ManagedPrefix, when non-empty, switches Run from "execute Command
	// as a shell line" to "install ManagedPrefixPkg into a PrAImate-owned
	// prefix dir named <ManagedPrefix> with a hoisted node-linker."
	// Command is still set for display but isn't executed verbatim.
	// Used for agents (openclaude) whose upstream package has a phantom
	// dependency that strict global pnpm refuses to resolve — see
	// installIntoManagedPrefix. ManagedPrefix is the subdir name (and
	// the agent's binary name); ManagedPrefixPkg is the npm spec.
	ManagedPrefix    string
	ManagedPrefixPkg string

	// DownloadAsset, when non-empty, switches Run to the NATIVE release-
	// asset downloader (DownloadReleaseAsset): fetch this asset from the
	// latest GitHub release into DownloadDest. No shell, no curl, no
	// PowerShell — Go's net/http handles TLS and retries, and a missing
	// asset returns ErrNoPrebuiltAsset instead of an opaque 404. Command
	// is still set for display but isn't executed.
	DownloadAsset string
	DownloadDest  string

	// VerifyRun, when true, makes Run probe `<DownloadDest> --version`
	// after a download/sidecar install so a binary that can't execute on
	// this machine fails the install loudly instead of "succeeding" and
	// then showing up as broken in detection forever. FallbackAsset,
	// when set, is downloaded in place of DownloadAsset if the probe
	// dies with an illegal instruction (Bun AVX2 build on a non-AVX2
	// CPU → the "-baseline" variant).
	VerifyRun     bool
	FallbackAsset string
}

// Shell tells Run how to execute Command. Direct means no shell wrapping
// (first word is the binary, rest are args, split on spaces — safe for
// the simple commands in our catalog).
type Shell string

const (
	ShellDirect     Shell = ""
	ShellBash       Shell = "bash"
	ShellPowerShell Shell = "powershell"
)

// AgentID matches launcher.AgentID. Kept as a separate string here so
// this package doesn't depend on the launcher package.
type AgentID string

const (
	AgentClaude      AgentID = "claude"
	AgentOpenClaude  AgentID = "openclaude"
	AgentCodex       AgentID = "codex"
	AgentCopilot     AgentID = "copilot"
	AgentAntigravity AgentID = "antigravity"
	AgentOpenCode    AgentID = "opencode"
)

// Action is "install" or "update". Update reuses most install commands,
// just with @latest where applicable.
type Action string

const (
	ActionInstall Action = "install"
	ActionUpdate  Action = "update"
)

// Methods returns the candidate install/update methods for an agent on
// the current OS, filtered to only those whose required package manager
// exists on PATH. The first entry in the slice is the recommended one.
func Methods(agent AgentID, action Action, current OS) []Method {
	all := allMethods(agent, action, current)
	var filtered []Method
	for _, m := range all {
		if !methodAvailable(m, current) {
			continue
		}
		filtered = append(filtered, m)
	}
	// Promote the recommended one to index 0 if it survived filtering.
	for i, m := range filtered {
		if m.Recommended {
			if i != 0 {
				filtered[0], filtered[i] = filtered[i], filtered[0]
			}
			break
		}
	}
	return filtered
}

// methodAvailable reports whether the package manager named by m.ID is on
// PATH OR is auto-fixable. The shell wrappers (curl|bash, powershell)
// only need curl / powershell, which we model as the ID itself.
//
// Auto-fixable runners (pnpm, today) survive this filter because Run()
// installs them on demand via corepack. Without this carve-out, a fresh
// Windows machine without pnpm sees "No install method available" for
// every Node-based agent (codex, opencode, gemini, deepseek), even
// though the launcher is capable of installing pnpm itself.
func methodAvailable(m Method, current OS) bool {
	if m.DownloadAsset != "" {
		return true // native Go downloader — no external runner needed
	}
	bin := m.ID
	switch bin {
	case "powershell":
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			return true
		}
		_, err := exec.LookPath("powershell")
		return err == nil
	default:
		if _, err := exec.LookPath(bin); err == nil {
			return true
		}
		// The GUI opts into RunOptions.InstallNode. On Windows that lets
		// winget install Node (and npm) before executing a Node-backed
		// method, so keep npm methods visible even on a fresh PATH. The UI
		// still reports/block missing prerequisites when winget is absent.
		if current == OSWindows && bin == "npm" && methodNeedsNode(m) {
			return true
		}
		// Keep the method visible if the missing runner is something the
		// launcher knows how to install. The TUI's prereq line ("will
		// auto-fix: pnpm" / "you must install: node") tells the user
		// what'll happen, which is far more helpful than silently
		// hiding every option.
		return autoFixablePrereqs[bin]
	}
}

// PrereqsMissing returns the subset of m.Prereqs not found on PATH. Used
// by the TUI to surface a "this method needs <x>" warning before running.
func PrereqsMissing(m Method) []string {
	var missing []string
	for _, p := range m.Prereqs {
		if _, err := exec.LookPath(p); err != nil {
			missing = append(missing, p)
		}
	}
	return missing
}

// autoFixablePrereqs lists prereqs the launcher can install for the user
// without invasive system changes. Run() resolves these automatically
// before running the install method. Anything not in this set requires
// user action (e.g. installing Node).
var autoFixablePrereqs = map[string]bool{
	"pnpm": true,
}

// AutoFixable returns the subset of `missing` that Run() will resolve
// automatically. The TUI uses this to render "will auto-install: X"
// instead of blocking the user.
func AutoFixable(missing []string) []string {
	var out []string
	for _, p := range missing {
		if autoFixablePrereqs[p] {
			out = append(out, p)
		}
	}
	return out
}

// UnfixableMissing returns the prereqs in `missing` that the user must
// install themselves (Node, mostly). The TUI blocks Enter when this is
// non-empty.
func UnfixableMissing(missing []string) []string {
	var out []string
	for _, p := range missing {
		if !autoFixablePrereqs[p] {
			out = append(out, p)
		}
	}
	return out
}

// InstallNodePossible reports whether the launcher can offer to install
// Node automatically on the current host. Currently true only on Windows
// when winget is on PATH. The TUI uses this to decide whether to render
// the "Also install Node.js" opt-in toggle on the install screen.
func InstallNodePossible() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	for _, name := range []string{"winget.exe", "winget"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

// BlockingPrereqs returns prerequisites that RunWithOptions cannot resolve
// for the caller. The GUI uses this instead of PrereqsMissing so a Windows
// npm method remains selectable when the user has winget and has opted into
// automatic Node installation.
func BlockingPrereqs(m Method, opts RunOptions) []string {
	missing := UnfixableMissing(PrereqsMissing(m))
	if !opts.InstallNode || runtime.GOOS != "windows" || !InstallNodePossible() || !methodNeedsNode(m) {
		return missing
	}
	out := missing[:0]
	for _, prerequisite := range missing {
		if prerequisite != "node" && prerequisite != "npm" {
			out = append(out, prerequisite)
		}
	}
	return out
}

// AutoInstallNodeWindows installs Node.js LTS via winget. Opt-in:
// callers should only invoke this when the user has explicitly toggled
// "also install Node" on the install screen. Returns env additions
// (updated PATH including the new node bin dir) so the immediately-
// following pnpm setup in the same process can find node + corepack
// without a shell restart.
//
// Windows-only by design — node install paths on macOS / Linux are too
// varied to auto-handle safely (homebrew vs nvm vs nodesource vs distro
// packages). Other OSes return an error pointing the user at nodejs.org.
func AutoInstallNodeWindows(ctx context.Context, w io.Writer) ([]string, error) {
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("auto-install Node is Windows-only; on %s install Node yourself from https://nodejs.org", runtime.GOOS)
	}
	wingetBin := ""
	for _, name := range []string{"winget.exe", "winget"} {
		if p, err := exec.LookPath(name); err == nil {
			wingetBin = p
			break
		}
	}
	if wingetBin == "" {
		return nil, fmt.Errorf("winget not on PATH — install App Installer from the Microsoft Store (https://aka.ms/getwinget) or install Node manually from https://nodejs.org")
	}
	fmt.Fprintln(w, "→ installing Node.js LTS via winget...")
	cmd := exec.CommandContext(ctx, wingetBin,
		"install", "--id", "OpenJS.NodeJS.LTS",
		"--silent",
		"--accept-source-agreements",
		"--accept-package-agreements")
	hideConsole(cmd)
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("winget install OpenJS.NodeJS.LTS failed: %w", err)
	}

	// Winget writes the new node bin dir to user/machine PATH in the
	// registry — that doesn't propagate to our running process. Probe
	// the standard locations and prepend the one we find to PATH so
	// later exec.LookPath("node") / exec.LookPath("corepack") succeed
	// immediately.
	candidates := []string{
		`C:\Program Files\nodejs`,
		os.ExpandEnv(`${LOCALAPPDATA}\Programs\nodejs`),
		os.ExpandEnv(`${ProgramFiles}\nodejs`),
	}
	nodeDir := ""
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, "node.exe")); err == nil {
			nodeDir = c
			break
		}
	}
	if nodeDir == "" {
		return nil, fmt.Errorf("Node installed via winget but its bin dir wasn't in the standard locations; restart Clade after the install completes")
	}
	fmt.Fprintf(w, "✓ Node bin dir: %s\n", nodeDir)
	return []string{
		"PATH=" + nodeDir + ";" + os.Getenv("PATH"),
	}, nil
}

// AutoInstallPnpm runs `corepack enable` so pnpm becomes available
// without an extra global install. It uses the PrAImate-managed bin dir
// to avoid permission issues with the system Node directory.
func AutoInstallPnpm(ctx context.Context, w io.Writer) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node is required to auto-install pnpm via corepack — install Node ≥ 20 first")
	}
	if _, err := exec.LookPath("corepack"); err != nil {
		return fmt.Errorf("corepack not on PATH — install a Node version that bundles corepack (≥ 16.10)")
	}

	binDir, err := PraimateBinDir()
	if err != nil {
		return fmt.Errorf("resolve PrAImate bin dir: %w", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create PrAImate bin dir %s: %w", binDir, err)
	}

	fmt.Fprintf(w, "→ enabling pnpm into %s...\n", binDir)
	var cap bytes.Buffer
	// corepack enable --install-directory puts shims into our managed bin dir
	// instead of trying to write to Node's own bin dir (which usually
	// requires sudo on Linux/macOS).
	cmd := exec.CommandContext(ctx, "corepack", "enable", "--install-directory", binDir)
	hideConsole(cmd)
	cmd.Stdout = io.MultiWriter(w, &cap)
	cmd.Stderr = io.MultiWriter(w, &cap)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("corepack enable failed: %w\n%s", err, pnpmFailureHint(cap.String(), "corepack enable"))
	}

	// Add the new bin dir to the current process PATH so we can run the
	// newly-created pnpm shim immediately for the config step.
	sep := string(os.PathListSeparator)
	_ = os.Setenv("PATH", binDir+sep+os.Getenv("PATH"))

	// Configure pnpm to use the same dir as its global-bin-dir so future
	// `pnpm add -g` commands (Gemini, DeepSeek, etc.) also land in our
	// managed prefix instead of failing on system permissions.
	fmt.Fprintln(w, "→ configuring pnpm global bin dir...")
	cfgCmd := exec.CommandContext(ctx, "pnpm", "config", "set", "global-bin-dir", binDir)
	hideConsole(cfgCmd)
	if _, err := cfgCmd.CombinedOutput(); err != nil {
		// Non-fatal: if this fails, EnsurePnpmReady will still try `pnpm setup`
		// which is a slower but viable fallback.
		fmt.Fprintf(w, "  (warning: pnpm config set failed: %v)\n", err)
	}

	return nil
}

// pnpmFailureHint inspects captured stdout+stderr from a corepack or
// `pnpm setup` invocation and returns a multi-line user-facing string
// that explains what to do next. We only match on stable substrings
// from known failure modes; the underlying error is still returned
// untouched (wrapped with %w) so power users can see the original
// stack.
//
// Known modes we recognise:
//   - ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING — corepack-bundled pnpm
//     crashes inside Node's ESM loader on certain Node 20.x builds.
//     The bug is in corepack (it uses dynamic imports without
//     registering the importModuleDynamically callback Node now
//     requires). Tell the user to skip corepack and install pnpm
//     directly.
//   - "Cannot find module 'pnpm'" / generic exit — fall through to a
//     short "install pnpm yourself" hint.
func pnpmFailureHint(captured, stage string) string {
	const directInstall = `To work around this, install pnpm directly (skips corepack):

    curl -fsSL https://get.pnpm.io/install.sh | sh -
    exec $SHELL              # reload PATH
    clade                    # re-run; installer will skip pnpm setup

Or on Windows / PowerShell:

    iwr https://get.pnpm.io/install.ps1 -useb | iex

Once pnpm is on PATH, Clade's installer detects it and won't invoke ` + "`pnpm setup`" + ` again.`

	if strings.Contains(captured, "ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING") {
		return "" +
			"This is a known incompatibility between corepack's bundled pnpm\n" +
			"shim and certain Node 20.x builds — the corepack wrapper uses a\n" +
			"dynamic import without registering Node's importModuleDynamically\n" +
			"callback, and Node aborts with ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING.\n" +
			"The bug is in corepack, not in pnpm or Clade.\n\n" +
			directInstall
	}
	return "Couldn't run " + stage + ". " + directInstall
}

// pnpmGlobalBinDir asks pnpm where it puts global bins. Returns an empty
// string when pnpm hasn't been set up yet — pnpm prints "undefined" in
// that case, which we normalize. Note: pnpm reads PNPM_HOME from this
// process's env, so a value written by an earlier `pnpm setup` (which
// lands in the Windows user registry / shell rc) is invisible until the
// process restarts. EnsurePnpmReady handles that gap.
func pnpmGlobalBinDir(ctx context.Context) string {
	pnpmCfg := exec.CommandContext(ctx, "pnpm", "config", "get", "global-bin-dir")
	hideConsole(pnpmCfg)
	out, err := pnpmCfg.Output()
	if err != nil {
		return ""
	}
	s := strings.TrimSpace(string(out))
	if s == "" || s == "undefined" {
		return ""
	}
	return s
}

// ImportPnpmPathIfPresent looks for the OS-default pnpm bin dir on disk;
// if it exists but isn't already in PATH, prepends it (and exports
// PNPM_HOME). Use this at launcher startup so a previously-installed
// agent (via `pnpm add -g …` in some past session) is found by
// exec.LookPath even when the user's shell hasn't picked up the env
// vars `pnpm setup` wrote to the Windows registry / shell rc.
//
// Safe to call repeatedly: no-op when the dir is already in PATH.
func ImportPnpmPathIfPresent() {
	dir := defaultPnpmHome()
	if dir == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // pnpm setup has never run
	}
	path := os.Getenv("PATH")
	// Case-insensitive compare on Windows (paths in PATH may have mixed case).
	cmp := func(a, b string) bool { return a == b }
	if runtime.GOOS == "windows" {
		cmp = strings.EqualFold
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	for _, entry := range strings.Split(path, sep) {
		if cmp(strings.TrimRight(entry, `\/`), strings.TrimRight(dir, `\/`)) {
			return // already on PATH
		}
	}
	_ = os.Setenv("PATH", dir+sep+path)
	if os.Getenv("PNPM_HOME") == "" {
		_ = os.Setenv("PNPM_HOME", dir)
	}
}

// defaultPnpmHome returns the OS-default location pnpm setup writes to.
// Used as a fallback when neither pnpm config get nor parsing the setup
// output gives us a value.
//
//	Windows: %LOCALAPPDATA%\pnpm
//	macOS:   ~/Library/pnpm
//	Linux:   $XDG_DATA_HOME/pnpm or ~/.local/share/pnpm
func defaultPnpmHome() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "pnpm")
		}
		if home != "" {
			return filepath.Join(home, "AppData", "Local", "pnpm")
		}
	case "darwin":
		if home != "" {
			return filepath.Join(home, "Library", "pnpm")
		}
	default:
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			return filepath.Join(xdg, "pnpm")
		}
		if home != "" {
			return filepath.Join(home, ".local", "share", "pnpm")
		}
	}
	return ""
}

var pnpmHomeRE = regexp.MustCompile(`(?m)^\s*PNPM_HOME\s*=\s*(.+?)\s*$`)

// extractPnpmHome scrapes the value out of `pnpm setup` output, which
// includes a line like:
//
//	PNPM_HOME=C:\Users\user\AppData\Local\pnpm
//
// on Windows and similar on other platforms.
func extractPnpmHome(setupStdout string) string {
	m := pnpmHomeRE.FindStringSubmatch(setupStdout)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// EnsurePnpmReady makes pnpm fully usable for a `pnpm add -g …` command
// in the same process:
//
//  1. If pnpm is missing, runs `corepack enable`.
//  2. If pnpm has no global-bin-dir configured (the "ERR_PNPM_NO_GLOBAL_BIN_DIR"
//     case on a fresh Windows install), runs `pnpm setup`.
//  3. Resolves PNPM_HOME using three sources in order:
//     a) `pnpm config get global-bin-dir`  (post-restart-of-shell case)
//     b) parse PNPM_HOME from the just-captured pnpm setup stdout
//     c) the OS-default location (%LOCALAPPDATA%\pnpm etc.)
//  4. Returns env additions (PNPM_HOME, PATH) so the upcoming install
//     command in *this* process sees the new bin dir without needing a
//     shell restart.
func EnsurePnpmReady(ctx context.Context, w io.Writer) ([]string, error) {
	if _, err := exec.LookPath("pnpm"); err != nil {
		fmt.Fprintln(w, "→ pnpm not on PATH; enabling via corepack...")
		if err := AutoInstallPnpm(ctx, w); err != nil {
			return nil, err
		}
	}

	binDir := pnpmGlobalBinDir(ctx)
	// If `pnpm config get` returned nothing, the user may still have a
	// working pnpm install whose PNPM_HOME just hasn't been propagated
	// to this process (common after `pnpm setup` in a prior session).
	// Probe PNPM_HOME and the OS-default home dir BEFORE invoking
	// `pnpm setup`, which on Node 20.x can crash with
	// ERR_VM_DYNAMIC_IMPORT_CALLBACK_MISSING through corepack's shim.
	if binDir == "" {
		if env := strings.TrimSpace(os.Getenv("PNPM_HOME")); env != "" {
			if fi, err := os.Stat(env); err == nil && fi.IsDir() {
				binDir = env
				fmt.Fprintf(w, "→ using existing PNPM_HOME: %s\n", binDir)
			}
		}
	}
	if binDir == "" {
		if def := defaultPnpmHome(); def != "" {
			if fi, err := os.Stat(def); err == nil && fi.IsDir() {
				binDir = def
				fmt.Fprintf(w, "→ pnpm already set up at %s — skipping `pnpm setup`\n", binDir)
			}
		}
	}
	if binDir == "" {
		fmt.Fprintln(w, "→ pnpm global bin dir not configured; running `pnpm setup`...")
		var setupCap bytes.Buffer
		cmd := exec.CommandContext(ctx, "pnpm", "setup")
		hideConsole(cmd)
		cmd.Stdout = io.MultiWriter(w, &setupCap)
		cmd.Stderr = io.MultiWriter(w, &setupCap)
		if err := cmd.Run(); err != nil {
			return nil, fmt.Errorf("pnpm setup failed: %w\n\n%s", err, pnpmFailureHint(setupCap.String(), "pnpm setup"))
		}
		// pnpm setup writes to user-level env (registry on Windows, rc
		// files on Unix) — those don't propagate to our running process.
		// Try config first, fall back to parsing setup output, then to
		// the OS default.
		binDir = pnpmGlobalBinDir(ctx)
		if binDir == "" {
			binDir = extractPnpmHome(setupCap.String())
		}
		if binDir == "" {
			binDir = defaultPnpmHome()
		}
		if binDir == "" {
			return nil, fmt.Errorf("pnpm setup ran but couldn't resolve PNPM_HOME; restart your shell and retry")
		}
		// pnpm setup creates the dir; verify it's there.
		if _, err := os.Stat(binDir); err != nil {
			return nil, fmt.Errorf("pnpm setup ran but %s doesn't exist: %w", binDir, err)
		}
		fmt.Fprintf(w, "✓ pnpm bin dir: %s\n", binDir)
	}

	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	return []string{
		"PNPM_HOME=" + binDir,
		"PATH=" + binDir + sep + os.Getenv("PATH"),
	}, nil
}

// Run executes the method's command line. stdout/stderr are streamed live
// so the TUI can render output as it arrives. Returns the OS exit error
// (nil on exit 0).
//
// For pnpm methods, Run auto-resolves the two common Windows footguns
// (pnpm not installed, pnpm setup not run) and injects PNPM_HOME + PATH
// into the install command's env. That makes `pnpm add -g …` work
// end-to-end without a shell restart.
// RunOptions are the per-invocation knobs for Run. Today only InstallNode
// is meaningful — the opt-in toggle on the install screen sets it.
// Other potential knobs (force re-install, skip prereq checks, etc.) can
// land here later without churning the signature again.
type RunOptions struct {
	// InstallNode opts the user into the Windows winget-based Node
	// install. Only honored when (a) we're on Windows and (b) the
	// method has node in Prereqs and node isn't already on PATH.
	InstallNode bool
}

// Run executes m with default options. Equivalent to RunWithOptions with
// a zero-valued RunOptions. Kept as a thin wrapper so callers that don't
// care about the opt-ins keep working unchanged.
func Run(ctx context.Context, m Method, stdout, stderr io.Writer) error {
	return RunWithOptions(ctx, m, RunOptions{}, stdout, stderr)
}

// RunWithOptions is Run + caller-supplied opt-ins. Streams stdout/stderr
// to the writers so the TUI can render output live.
func RunWithOptions(ctx context.Context, m Method, opts RunOptions, stdout, stderr io.Writer) error {
	// Native release-asset install — handled fully in-process, before
	// any prereq/env work (it needs no node, pnpm, curl or PowerShell).
	// A binary bundled next to the running praimate executable (release
	// archives ship praimate-code / praimate-graphify there) wins over
	// the network.
	if m.DownloadAsset != "" {
		if handled, err := installFromBundledSidecar(m, stdout); handled {
			if err != nil {
				return err
			}
			return verifyInstalledBinary(ctx, m, stdout)
		}
		if err := DownloadReleaseAsset(ctx, m.DownloadAsset, m.DownloadDest, stdout); err != nil {
			return err
		}
		return verifyInstalledBinary(ctx, m, stdout)
	}
	var extraEnv []string
	// Honor the Node opt-in first — if pnpm setup follows, it needs
	// node on PATH or it can't run corepack at all. Gated on the method
	// actually declaring node as a prereq: without this, a caller that
	// always passes InstallNode (the GUI does) triggered a multi-minute
	// winget Node install before methods that never touch Node, like the
	// prebuilt-binary downloads.
	if opts.InstallNode && runtime.GOOS == "windows" && methodNeedsNode(m) {
		if _, err := exec.LookPath("node"); err != nil {
			env, err := AutoInstallNodeWindows(ctx, stdout)
			if err != nil {
				return err
			}
			extraEnv = append(extraEnv, env...)
			// Mirror onto our process env so the subsequent
			// EnsurePnpmReady's LookPath("node") / LookPath("corepack")
			// see the new bin dir.
			for _, kv := range env {
				if i := strings.Index(kv, "="); i > 0 {
					_ = os.Setenv(kv[:i], kv[i+1:])
				}
			}
		}
	}
	if strings.HasPrefix(m.Command, "pnpm ") {
		env, err := EnsurePnpmReady(ctx, stdout)
		if err != nil {
			return err
		}
		extraEnv = append(extraEnv, env...)
	}
	if strings.HasPrefix(m.Command, "uv ") {
		env, err := EnsureUvReady(ctx, stdout)
		if err != nil {
			return err
		}
		extraEnv = append(extraEnv, env...)
	}
	// Managed-prefix install: dispatch by installer kind. pnpm methods
	// land under clade/agents/<name>/ with a project-local hoisted
	// node-linker (openclaude). Tool methods land under clade/tools/<name>/
	// with installer-specific handling (uv tool, git+setup, uv venv).
	// Bypasses buildCmd entirely.
	if m.ManagedPrefix != "" {
		switch m.ManagedPrefix {
		case "graphify":
			return installUvIntoManagedPrefix(ctx, m, extraEnv, stdout, stderr)
		case "gstack":
			return installGstackIntoManagedPrefix(ctx, m, extraEnv, stdout, stderr)
		case "scrapegraph":
			return installScrapeGraphIntoManagedVenv(ctx, m, extraEnv, stdout, stderr)
		default:
			return installIntoManagedPrefix(ctx, m, extraEnv, stdout, stderr)
		}
	}
	cmd := buildCmd(ctx, m)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
		// Also mirror the additions onto OUR process env so subsequent
		// exec.LookPath calls (the post-install agent re-detection, the
		// eventual launch) see the new pnpm bin dir without needing the
		// user to restart their shell.
		for _, kv := range extraEnv {
			if i := strings.Index(kv, "="); i > 0 {
				_ = os.Setenv(kv[:i], kv[i+1:])
			}
		}
	}
	return cmd.Run()
}

// methodNeedsNode reports whether running m depends on a Node runtime —
// either it declares node as a prereq or it's a pnpm command (pnpm's
// corepack bootstrap needs node too).
func methodNeedsNode(m Method) bool {
	for _, p := range m.Prereqs {
		if p == "node" {
			return true
		}
	}
	return strings.HasPrefix(m.Command, "pnpm ")
}

func buildCmd(ctx context.Context, m Method) *exec.Cmd {
	switch m.Shell {
	case ShellBash:
		cmd := exec.CommandContext(ctx, "bash", "-c", m.Command)
		hideConsole(cmd)
		return cmd
	case ShellPowerShell:
		bin := "powershell"
		if _, err := exec.LookPath("powershell.exe"); err == nil {
			bin = "powershell.exe"
		}
		// Windows PowerShell 5.1's .NET web stack can default to TLS 1.0,
		// which GitHub / claude.ai reject mid-handshake ("The connection
		// was closed unexpectedly"). Opt the spawned session into TLS 1.2+
		// before the actual command runs.
		const tlsBootstrap = "try { [Net.ServicePointManager]::SecurityProtocol = " +
			"[Net.ServicePointManager]::SecurityProtocol -bor 3072 -bor 12288 } catch {}; "
		cmd := exec.CommandContext(ctx, bin, "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", tlsBootstrap+m.Command)
		hideConsole(cmd)
		return cmd
	default:
		parts := strings.Fields(m.Command)
		cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
		hideConsole(cmd)
		return cmd
	}
}

// ManagedAgentPrefix returns the PrAImate-owned directory where an agent
// with a non-standard install layout lives, alongside PrAImate's own
// config under os.UserConfigDir()/clade/agents/<name>/. The agent's
// executable ends up at <prefix>/node_modules/.bin/<name>. Used for
// agents (openclaude) whose upstream package can't be installed cleanly
// via `pnpm add -g` — see installIntoManagedPrefix.
func ManagedAgentPrefix(name string) (string, error) {
	base, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "agents", name), nil
}

// ManagedAgentBinDir returns <prefix>/node_modules/.bin for the named
// managed agent — the dir pnpm drops the executable (and its .CMD /
// .ps1 shims on Windows) into. Agent detection probes here.
func ManagedAgentBinDir(name string) (string, error) {
	prefix, err := ManagedAgentPrefix(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(prefix, "node_modules", ".bin"), nil
}

// installIntoManagedPrefix installs m.ManagedPrefixPkg into the
// PrAImate-owned prefix dir named m.ManagedPrefix using a PROJECT-LOCAL
// `pnpm add` (not `-g`). The prefix gets a private package.json (so
// pnpm treats it as a standalone project and doesn't walk up to a
// parent workspace) and a `.npmrc` carrying:
//
//	node-linker=hoisted     → flat, npm-like node_modules so openclaude's
//	                          undeclared @aws-sdk/client-bedrock-runtime
//	                          import resolves (the phantom-dep fix). Honored
//	                          for project-local installs, unlike `pnpm add -g`.
//	registry=...npmjs.org/  → supply-chain pin, same as pnpmRegistryFlag.
//
// extraEnv carries the PNPM_HOME / PATH additions EnsurePnpmReady built
// so pnpm is usable in-process without a shell restart.
func installIntoManagedPrefix(ctx context.Context, m Method, extraEnv []string, stdout, stderr io.Writer) error {
	prefix, err := ManagedAgentPrefix(m.ManagedPrefix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return fmt.Errorf("create managed prefix %s: %w", prefix, err)
	}
	fmt.Fprintf(stdout, "→ installing %s into Clade-managed prefix: %s\n", m.ManagedPrefixPkg, prefix)

	// Private host manifest — marks this a standalone project so pnpm
	// doesn't resolve against an ancestor workspace and so `pnpm add`
	// has a package.json to record the dep in.
	pkgJSON := `{
  "name": "clade-` + m.ManagedPrefix + `-host",
  "version": "0.0.0",
  "private": true,
  "description": "Clade-managed install prefix for ` + m.ManagedPrefix + `. Do not edit by hand."
}
`
	if err := os.WriteFile(filepath.Join(prefix, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		return fmt.Errorf("write host package.json: %w", err)
	}
	// .npmrc pins the registry for the prefix (supply-chain). NOTE the
	// node-linker is NOT set here: pnpm v11 silently ignores a
	// `node-linker=` line in a project .npmrc (verified) — it's only
	// honored as the `--config.node-linker=hoisted` command flag we
	// pass below. We still drop the registry line so any later manual
	// `pnpm` run in this dir stays pinned.
	npmrc := "registry=https://registry.npmjs.org/\n"
	if err := os.WriteFile(filepath.Join(prefix, ".npmrc"), []byte(npmrc), 0o644); err != nil {
		return fmt.Errorf("write prefix .npmrc: %w", err)
	}

	// --config.node-linker=hoisted: flat node_modules so the agent's
	//   undeclared transitive import (openclaude → @aws-sdk/client-
	//   bedrock-runtime) resolves. Honored as a flag for project-local
	//   installs, unlike a .npmrc line or `pnpm add -g`.
	// Build scripts stay BLOCKED (pnpm 10+ default): we deliberately do
	//   NOT pass --config.dangerouslyAllowAllBuilds or approve sharp/
	//   protobufjs. That's the supply-chain posture — and openclaude
	//   runs fine without those native builds (its launch path doesn't
	//   touch them). pnpm flags this with ERR_PNPM_IGNORED_BUILDS and a
	//   non-zero exit; we treat that specific case as success below.
	var cap bytes.Buffer

	// Prefer npm for local managed installs: it natively hoists (preventing
	// the phantom dep issue) and bypasses Corepack pnpm v11 dynamic import
	// bugs on Node 20.19+.
	var cmd *exec.Cmd
	if _, err := exec.LookPath("npm"); err == nil {
		cmd = exec.CommandContext(ctx, "npm", "install", "--no-fund", "--no-audit", "--ignore-scripts", m.ManagedPrefixPkg)
	} else {
		cmd = exec.CommandContext(ctx, "pnpm", "add",
			"--config.node-linker=hoisted",
			"--ignore-scripts",
			m.ManagedPrefixPkg)
	}

	hideConsole(cmd)
	cmd.Dir = prefix
	cmd.Stdout = io.MultiWriter(stdout, &cap)
	cmd.Stderr = io.MultiWriter(stderr, &cap)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	runErr := cmd.Run()

	// Success criterion is the binary existing, not pnpm's exit code:
	// pnpm returns non-zero purely because it ignored sharp/protobufjs
	// build scripts (the supply-chain default we WANT). If the bin is
	// present, the install worked; the ignored builds aren't needed at
	// launch.
	bin := filepath.Join(prefix, "node_modules", ".bin", m.ManagedPrefix)
	binPresent := false
	for _, cand := range []string{bin, bin + ".CMD", bin + ".cmd", bin + ".ps1"} {
		if _, err := os.Stat(cand); err == nil {
			binPresent = true
			break
		}
	}
	if runErr != nil {
		ignoredBuildsOnly := strings.Contains(cap.String(), "ERR_PNPM_IGNORED_BUILDS")
		if !(binPresent && ignoredBuildsOnly) {
			return fmt.Errorf("install %s into %s: %w\nOutput: %s", m.ManagedPrefixPkg, prefix, runErr, cap.String())
		}
		fmt.Fprintln(stdout, "  (package manager blocked postinstall build scripts — left unbuilt by design; "+
			m.ManagedPrefix+" does not need them at launch)")
	}
	if !binPresent {
		return fmt.Errorf("install %s succeeded but %s binary not found under %s/node_modules/.bin",
			m.ManagedPrefixPkg, m.ManagedPrefix, prefix)
	}
	fmt.Fprintf(stdout, "✓ %s installed; binary at %s\n", m.ManagedPrefix, bin)
	return nil
}

// pnpmRegistryFlag pins the registry on every global pnpm install we
// emit. Defends against three real-world attack vectors:
//
//  1. A poisoned ~/.npmrc or repo-local .npmrc that redirects `registry=`
//     to a typosquatted mirror.
//  2. An attacker-set npm_config_registry env var inherited from the
//     user's shell.
//  3. CI environments where the registry default was changed at the
//     image level.
//
// Pinning here forces every pnpm command PrAImate emits through the
// canonical registry regardless of those config layers. Users who
// LEGITIMATELY use a private registry (Verdaccio, JFrog) install the
// agent themselves outside PrAImate; the installer's job is to make the
// default path safe, not to be configurable for proxied installs.
//
// Note: we do NOT add --ignore-scripts globally. Several of the CLIs
// here (codex, opencode native binaries) need their postinstall to
// fetch the platform binary; --ignore-scripts would leave the install
// broken. The supply-chain risk is real but the mitigation breaks
// functionality. A future hardening could ignore scripts on a per-
// package allowlist once we audit each agent's postinstall.
const pnpmRegistryFlag = " --registry=https://registry.npmjs.org/"

// allMethods is the static catalog, before filtering by what's installed.
// Mirror of agent-cli-installer.sh but pnpm-first and Windows-aware.
func allMethods(agent AgentID, action Action, current OS) []Method {
	npmPkg := func(pkg string) string {
		cmd := "npm install -g --no-fund --no-audit"
		if action == ActionUpdate {
			return cmd + " " + pkg + "@latest"
		}
		return cmd + " " + pkg
	}

	// OpenClaude is a single-maintainer npm package, which is a known
	// supply-chain risk profile (maintainer-account takeover → patch
	// published under the same name; chalk/debug Sept 2025 followed
	// this exact pattern). Real mitigations available to us:
	//
	//   - Registry pinning (pnpmRegistryFlag) — defends against
	//     ~/.npmrc / env redirection. Always on.
	//   - Version pinning — would defend against post-compromise
	//     patches, but requires the PrAImate maintainer to vet+bump the
	//     pin on every release. Not done yet: pinning to a stale
	//     version with no upgrade plan is a worse UX than @latest
	//     because users miss real bug fixes; pinning a placeholder
	//     version we never verified is a footgun.
	//   - --ignore-scripts — would block the postinstall vector but
	//     also breaks OpenClaude's native-binary download. Not done.
	//
	// Tracking item: once PrAImate has a documented "vet this agent's
	// release before bumping the pin" workflow, switch from @latest
	// to a pin on install (keeping @latest on update for the
	// opt-back-in case).
	//
	// KNOWN ISSUE — phantom dependency. OpenClaude (verified 0.13.0 +
	// 0.14.0) imports `@aws-sdk/client-bedrock-runtime` as runtime
	// values in dist/cli.mjs (its Bedrock gateway) but only declares
	// `@anthropic-ai/bedrock-sdk` in package.json. Under npm's flat
	// node_modules the undeclared import resolves by accident; under
	// pnpm's strict symlinked layout it fails at gateway-init with
	// `ERR_MODULE_NOT_FOUND: @aws-sdk/client-bedrock-runtime` — i.e.
	// openclaude crashes on launch.
	//
	// We CANNOT fix this from a `pnpm add -g` command: pnpm ignores
	// --node-linker / --config.node-linker / --shamefully-hoist /
	// npm_config_node_linker for global installs (verified empirically
	// — the global layout stays strict regardless). What DOES honor a
	// hoisted node-linker is a project-local install. So we install
	// openclaude into a PrAImate-owned prefix dir (see ManagedAgentPrefix
	// + installIntoManagedPrefix) with a `.npmrc` carrying
	// `node-linker=hoisted`, which flat-hoists the transitive
	// @aws-sdk/client-bedrock-runtime to where openclaude's import can
	// find it. The global pnpm config is untouched, so the other node
	// agents (codex/opencode/gemini/deepseek) keep their strict layout.
	//
	// openclaudePkg is the spec; openclaudeDisplayCmd is what we show
	// the user (it isn't executed verbatim — Run special-cases
	// ManagedPrefix). The registry flag is shown for transparency and
	// is also written into the prefix .npmrc.
	const openclaudePkg = "@gitlawb/openclaude@latest"
	openclaudeDisplayCmd := "npm install --no-fund --no-audit --ignore-scripts " + openclaudePkg

	switch agent {
	case AgentCopilot:
		return []Method{{ID: "npm", Label: "Official npm package", Command: npmPkg("@github/copilot"), Recommended: true, Prereqs: []string{"node", "npm"}}}
	case AgentAntigravity:
		if current == OSWindows {
			return []Method{{ID: "powershell", Label: "Official Antigravity installer", Command: "irm https://antigravity.google/cli/install.ps1 | iex", Shell: ShellPowerShell, Recommended: true}}
		}
		if current == OSLinux || current == OSWSL || current == OSMacOS {
			return []Method{{ID: "curl", Label: "Official Antigravity installer", Command: "curl -fsSL https://antigravity.google/cli/install.sh | bash", Shell: ShellBash, Recommended: true}}
		}
		return nil
	case AgentID("praimate-cli"):
		return praimateCLIMethods(current)
	case AgentID("praimate-code"):
		// PrAImate Code isn't pip/npm installable — it's our prebuilt
		// standalone, downloaded from the GitHub release. Reuse the
		// Tool download methods so the agent-install screen works when a
		// chat picks praimate-code before it's installed.
		return praimateCodeMethods(current)
	case AgentClaude:
		switch current {
		case OSMacOS:
			return []Method{
				{ID: "brew", Label: "Homebrew cask", Command: caskCmd(action, "claude-code"), Recommended: true},
				{ID: "curl", Label: "Official install script", Command: "curl -fsSL https://claude.ai/install.sh | bash", Shell: ShellBash},
			}
		case OSLinux, OSWSL:
			return []Method{
				{ID: "curl", Label: "Official install script", Command: "curl -fsSL https://claude.ai/install.sh | bash", Shell: ShellBash, Recommended: true},
			}
		case OSWindows:
			return []Method{
				{ID: "winget", Label: "winget package", Command: wingetCmd(action, "Anthropic.ClaudeCode"), Recommended: true},
				{ID: "powershell", Label: "PowerShell installer", Command: "irm https://claude.ai/install.ps1 | iex", Shell: ShellPowerShell},
			}
		}

	case AgentOpenClaude:
		// OpenClaude has no brew formula, winget package, or curl|bash
		// installer. Install it with npm into a PrAImate-managed prefix;
		// npm's hoisted layout works around the phantom aws-sdk dependency
		// described above without relying on Corepack/pnpm.
		return []Method{
			{ID: "npm",
				Label:            "npm into PrAImate-managed prefix",
				Command:          openclaudeDisplayCmd,
				ManagedPrefix:    "openclaude",
				ManagedPrefixPkg: openclaudePkg,
				Recommended:      true,
				Prereqs:          []string{"node", "npm"}},
		}

	case AgentCodex:
		switch current {
		case OSMacOS:
			return []Method{
				{ID: "brew", Label: "Homebrew formula", Command: formulaCmd(action, "codex"), Recommended: true},
				{ID: "npm", Label: "npm global package", Command: npmPkg("@openai/codex"), Prereqs: []string{"node", "npm"}},
			}
		case OSLinux, OSWSL, OSWindows:
			return []Method{
				{ID: "npm", Label: "npm global package", Command: npmPkg("@openai/codex"), Recommended: true, Prereqs: []string{"node", "npm"}},
				{ID: "brew", Label: "Homebrew formula", Command: formulaCmd(action, "codex")},
			}
		}

	case AgentOpenCode:
		switch current {
		case OSMacOS:
			return []Method{
				{ID: "curl", Label: "Official install script", Command: "curl -fsSL https://opencode.ai/install | bash", Shell: ShellBash, Recommended: true},
				{ID: "brew", Label: "Homebrew tap", Command: tapCmd(action, "anomalyco/tap/opencode")},
				{ID: "npm", Label: "npm global package", Command: npmPkg("opencode-ai"), Prereqs: []string{"node", "npm"}},
			}
		case OSLinux, OSWSL:
			return []Method{
				{ID: "curl", Label: "Official install script", Command: "curl -fsSL https://opencode.ai/install | bash", Shell: ShellBash, Recommended: true},
				{ID: "npm", Label: "npm global package", Command: npmPkg("opencode-ai"), Prereqs: []string{"node", "npm"}},
				{ID: "paru", Label: "AUR via paru", Command: paruCmd(action, "opencode")},
			}
		case OSWindows:
			// Offer the official curl|bash installer on Windows too,
			// when a Bash shell is on PATH (Git Bash / WSL / Cygwin).
			// The opencode-ai npm package has shipped Windows binaries
			// that some hosts reject ("not compatible with this version
			// of Windows"); the official script downloads a native
			// build that works around it. Recommend when both bash and
			// curl exist.
			methods := []Method{
				{ID: "curl", Label: "Official install script (needs bash + curl)",
					Command: "curl -fsSL https://opencode.ai/install | bash",
					Shell:   ShellBash, Recommended: true},
				{ID: "npm", Label: "npm global package",
					Command: npmPkg("opencode-ai"),
					Prereqs: []string{"node", "npm"}},
			}
			// If bash isn't on PATH the curl method will be filtered
			// out; demote pnpm to recommended in that case.
			if _, err := exec.LookPath("bash"); err != nil {
				methods = methods[1:]
				methods[0].Recommended = true
			}
			return methods
		}

	}
	return nil
}

func caskCmd(a Action, name string) string {
	if a == ActionUpdate {
		return "brew upgrade --cask " + name
	}
	return "brew install --cask " + name
}

func formulaCmd(a Action, name string) string {
	if a == ActionUpdate {
		return "brew upgrade " + name
	}
	return "brew install " + name
}

func tapCmd(a Action, name string) string {
	if a == ActionUpdate {
		return "brew upgrade " + name
	}
	return "brew install " + name
}

func wingetCmd(a Action, id string) string {
	// Both verbs carry the agreement flags: winget prompts interactively
	// without them, which hangs when we spawn it with no console input.
	if a == ActionUpdate {
		return "winget upgrade --id " + id + " -e --accept-package-agreements --accept-source-agreements"
	}
	return "winget install --id " + id + " -e --accept-package-agreements --accept-source-agreements"
}

func paruCmd(a Action, name string) string {
	if a == ActionUpdate {
		return "paru -Syu " + name
	}
	return "paru -S " + name
}
