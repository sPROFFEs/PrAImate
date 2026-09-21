package installer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/gitutil"
	"git.jtsec.local/lab/PrAImate/internal/version"
)

// ToolID names a non-agent capability PrAImate can install — a CLI the
// agent calls but the user doesn't launch as a primary session. Lives
// alongside AgentID so the installer can offer install/update for both
// without polluting the agent picker.
type ToolID string

const (
	// ToolGraphify is the knowledge-graph builder for codebases / docs.
	// Installed into a PrAImate-managed prefix via `uv tool install` so the
	// install is isolated from the user's Python environment.
	ToolGraphify ToolID = "graphify"
	// ToolGstack is Garry Tan's skill pack. It is not a normal binary
	// helper: setup installs slash-command skills into supported hosts
	// (Claude Code, Codex, OpenCode, etc.). PrAImate keeps the git checkout
	// under clade/tools/gstack and runs upstream setup from there.
	ToolGstack ToolID = "gstack"
	// ToolScrapeGraph installs a tiny PrAImate wrapper around ScrapeGraphAI's
	// Python packages so every agent can call `scrapegraph-search`.
	ToolScrapeGraph ToolID = "scrapegraph"
	// ToolPraimateCode is PrAImate Code — our version-pinned, rebranded
	// build of OpenCode. Unlike the others it isn't pip/npm installable;
	// the install downloads the prebuilt standalone binary from our
	// GitHub release into <config>/praimate/bin/, where `praimate code`
	// looks for it.
	ToolPraimateCode ToolID = "praimate-code"
	// ToolPraimateStudio is PrAImate Studio — our managed Code-OSS IDE
	// distribution with built-in PrAImate Core extension.
	ToolPraimateStudio ToolID = "praimate-studio"
)

// PraimateBinDir is <config>/praimate/bin — where managed standalone
// binaries (praimate-code) are installed and where the `praimate code`
// dispatcher looks for them.
func PraimateBinDir() (string, error) {
	base, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "bin"), nil
}

// Tool describes an installable non-agent capability. Same shape as
// Agent so the installer UI can render either with one code path; only
// the catalog (ToolMethods vs Methods) differs.
type Tool struct {
	ID          ToolID
	Label       string
	Binary      string
	InstallHint string
	Available   bool
	Version     string
	ProbeError  string
}

// KnownTools returns the static tool catalog. Availability is filled by
// DetectTools.
func KnownTools() []Tool {
	return []Tool{
		{
			ID:          ToolGraphify,
			Label:       "Graphify (knowledge-graph builder)",
			Binary:      "graphify",
			InstallHint: "uv tool install graphifyy   (needs uv on PATH; Clade installs into a managed prefix)",
		},
		{
			ID:          ToolGstack,
			Label:       "gstack (AI workflow skill pack)",
			Binary:      "gstack",
			InstallHint: "git clone + ./setup --host auto   (needs git + bun; skills work in Claude/Codex/OpenCode/Gemini, not DeepSeek)",
		},
		{
			ID:          ToolScrapeGraph,
			Label:       "ScrapeGraphAI search helper",
			Binary:      "scrapegraph-search",
			InstallHint: "uv venv + scrapegraphai/scrapegraph-py   (set SGAI_API_KEY for API mode, or SCRAPEGRAPH_LLM_MODEL for OSS/local mode)",
		},
		{
			ID:          ToolPraimateStudio,
			Label:       "PrAImate Studio (Code-OSS IDE)",
			Binary:      "praimate-studio",
			InstallHint: "Managed Code-OSS distribution with native PrAImate Core extension, terminal and Open VSX support",
		},
		// PrAImate Code is intentionally NOT here — it's a coding CLI, not
		// a companion tool, so it's surfaced in the CLIs browser instead.
		// Its install methods (ToolPraimateCode) still live in this package
		// and are reached from the CLIs screen.
	}
}

// DetectTools mirrors DetectAgents: probes each known tool by trying its
// binary on PATH AND its PrAImate-managed prefix, taking the first that
// produces a clean --version exit.
func DetectTools(ctx context.Context) []Tool {
	tools := KnownTools()
	for i := range tools {
		candidates := toolCandidatePaths(tools[i].ID, tools[i].Binary)
		var lastErr error
		for _, cand := range candidates {
			if st, err := os.Stat(cand); err != nil || st.IsDir() {
				continue
			}
			version, perr := probeToolVersion(ctx, cand)
			if perr != nil {
				if lastErr == nil {
					lastErr = perr
				}
				continue
			}
			tools[i].Available = true
			tools[i].Version = version
			tools[i].Binary = cand
			break
		}
		if !tools[i].Available && lastErr != nil {
			tools[i].ProbeError = lastErr.Error()
		}
	}
	return tools
}

func toolCandidatePaths(id ToolID, binary string) []string {
	var paths []string
	// PATH first, in case the user installed by hand.
	if p, err := exec.LookPath(binary); err == nil {
		paths = append(paths, p)
	}

	// Managed standalone bin dir second (bundled exes like praimate-code, graphify)
	if binDir, err := PraimateBinDir(); err == nil {
		bins := []string{binary}
		if runtime.GOOS == "windows" {
			bins = append(bins, binary+".exe")
		}
		for _, b := range bins {
			paths = append(paths, filepath.Join(binDir, b))
		}
	}

	if id == ToolPraimateCode {
		return paths // praimate-code has no managed tool prefix (no node_modules/uv_tool_dir)
	}
	if id == ToolPraimateStudio {
		if root, err := appdata.Root(); err == nil {
			sDir := filepath.Join(root, "tools", "praimate-studio")
			binName := "praimate-studio"
			if runtime.GOOS == "windows" {
				binName = "praimate-studio.cmd"
			}
			paths = append(paths, filepath.Join(sDir, "bin", binName))
			if runtime.GOOS != "windows" {
				paths = append(paths, filepath.Join(sDir, "app", "bin", "codium"), filepath.Join(sDir, "app", "codium"))
			} else {
				paths = append(paths, filepath.Join(sDir, "app", "bin", "codium.cmd"), filepath.Join(sDir, "app", "VSCodium.exe"))
			}
		}
		return paths
	}

	// Managed prefixes third: current (praimate) then legacy (clade) —
	// tools installed before the rebrand must keep detecting.
	bins := []string{binary}
	if runtime.GOOS == "windows" {
		bins = append(bins, binary+".exe", binary+".cmd", binary+".bat")
	}
	if binDir, err := ManagedToolBinDir(string(id)); err == nil {
		for _, b := range bins {
			paths = append(paths, filepath.Join(binDir, b))
		}
	}
	if base, err := os.UserConfigDir(); err == nil {
		legacy := filepath.Join(base, "clade", "tools", string(id), "bin")
		for _, b := range bins {
			paths = append(paths, filepath.Join(legacy, b))
		}
	}
	return paths
}

// errProbeTimeout marks a --version probe that hit its deadline: the
// binary exists and runs but is slow to start. Post-install
// verification treats this as "probably fine" rather than a broken
// install (Defender scanning a freshly written 170MB exe can push the
// first start past the deadline).
var errProbeTimeout = errors.New("--version probe timed out")

// probeToolVersion matches probeVersion in internal/launcher/agents.go:
// 8s deadline (Node / uv tools on Windows can take 3-6s to cold-start),
// with the timeout-vs-real-failure distinction surfaced as a readable
// error instead of the bare "signal: killed".
func probeToolVersion(parent context.Context, path string) (string, error) {
	const deadline = 8 * time.Second
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	probe := exec.CommandContext(ctx, path, "--version")
	hideConsole(probe)
	out, err := probe.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("%w after %s (binary is slow to start, "+
				"not necessarily broken — try invoking it directly to confirm)", errProbeTimeout, deadline)
		}
		if IsIllegalInstruction(err) {
			return "", fmt.Errorf("%w (illegal instruction — CPU lacks AVX2; reinstall to get the baseline build)", err)
		}
		// Keep what the binary printed before dying — a Bun crash
		// report's first line names the actual fault (GC bug, missing
		// CPU feature caught by its crash handler, ...), which the bare
		// "exit status 3" hides.
		if msg := firstOutputLine(out); msg != "" {
			return "", fmt.Errorf("%w — %s", err, msg)
		}
		return "", err
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	return strings.TrimSpace(line), nil
}

// firstOutputLine returns the first non-empty line of a probe's
// combined output, capped for display in error strings.
func firstOutputLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(line) > 160 {
			line = line[:157] + "..."
		}
		return line
	}
	return ""
}

// ManagedToolPrefix returns the PrAImate-owned directory where a managed
// tool lives, alongside PrAImate's own config under
// os.UserConfigDir()/clade/tools/<name>/. Parallel to ManagedAgentPrefix
// but a separate subtree so an agent named "graphify" couldn't ever
// collide with a tool named "graphify".
func ManagedToolPrefix(name string) (string, error) {
	base, err := appdata.Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "tools", name), nil
}

// ManagedToolBinDir returns <prefix>/bin — the dir uv writes the
// tool's entrypoint shim into when invoked with UV_TOOL_BIN_DIR set
// to that path. Probed by tool detection.
func ManagedToolBinDir(name string) (string, error) {
	prefix, err := ManagedToolPrefix(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(prefix, "bin"), nil
}

// GraphifyPinnedVersion is the graphify version PrAImate's RAG
// integration is verified against. The CLIs-tab installer pins this
// exact version into the managed prefix, and ResolveGraphify prefers
// that install — so an upstream change to graphify's CLI or output
// layout can't break us. Bump only after re-verifying extract + query
// + the graphify-out/ layout against the new version.
const GraphifyPinnedVersion = "0.8.36"

// bundledGraphifyName is the file name of the self-contained graphify
// binary PrAImate ships (PyInstaller-frozen, no Python needed). Lives in
// the praimate bin dir next to praimate-code.
func bundledGraphifyName() string {
	n := "praimate-graphify"
	if runtime.GOOS == "windows" {
		n += ".exe"
	}
	return n
}

// BundledGraphifyPath returns where the shipped standalone graphify
// binary lives (<config>/praimate/bin/praimate-graphify), whether or not
// it exists yet.
func BundledGraphifyPath() (string, error) {
	binDir, err := PraimateBinDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(binDir, bundledGraphifyName()), nil
}

// ResolveGraphify returns the absolute path to the graphify binary to
// use, in robustness order:
//
//  1. PrAImate's BUNDLED standalone build (no Python/uv/PATH needed) —
//     our most reliable known-good fallback.
//  2. The pinned uv-managed install in the praimate prefix.
//  3. The legacy clade-managed prefix.
//  4. Whatever's on PATH.
//
// Returns ("", false) when graphify can't be found anywhere.
func ResolveGraphify() (string, bool) {
	if p, err := BundledGraphifyPath(); err == nil && fileExists(p) {
		return p, true
	}
	name := "graphify"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if binDir, err := ManagedToolBinDir("graphify"); err == nil {
		if p := filepath.Join(binDir, name); fileExists(p) {
			return p, true
		}
	}
	if base, err := os.UserConfigDir(); err == nil {
		if p := filepath.Join(base, "clade", "tools", "graphify", "bin", name); fileExists(p) {
			return p, true
		}
	}
	if p, err := exec.LookPath("graphify"); err == nil {
		return p, true
	}
	return "", false
}

// graphifyAssetName is the release-asset file name for the bundled
// standalone graphify on the current OS/arch.
func graphifyAssetName() string {
	name := fmt.Sprintf("praimate-graphify-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// graphifyAssetShipped reports whether a prebuilt praimate-graphify asset
// exists in the release for the current OS/arch. PyInstaller can't
// cross-compile, so we add platforms here as they're built on native
// hosts. Until a platform is listed, its users install graphify via the
// pinned uv method instead (still robust — same vetted version).
func graphifyAssetShipped() bool {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return true
	default:
		return false
	}
}

// graphifyBundledMethod builds the download-the-prebuilt-binary install
// method for graphify — mirrors praimateCodeMethods: pull the shipped
// standalone build from the GitHub release into <config>/praimate/bin.
// Returns nil when no asset ships for this platform or the praimate bin
// dir can't be resolved.
func graphifyBundledMethod(current OS) *Method {
	if !graphifyAssetShipped() {
		return nil
	}
	binDir, err := PraimateBinDir()
	if err != nil {
		return nil
	}
	dest := filepath.Join(binDir, "graphify")
	if current == OSWindows {
		dest += ".exe"
	}
	asset := graphifyAssetName()
	return &Method{
		ID:            "download",
		Label:         "Download PrAImate's bundled graphify (no Python needed)",
		Command:       assetDownloadDisplayCmd(asset, dest),
		DownloadAsset: asset,
		DownloadDest:  dest,
		Recommended:   true,
	}
}

// assetDownloadDisplayCmd is the human-readable line the picker shows
// for a native download method. Not executed — Run dispatches on
// DownloadAsset and downloads in-process via net/http.
func assetDownloadDisplayCmd(asset, dest string) string {
	return "download " + version.RepoURL + "/releases/download/<latest>/" + asset + " -> " + dest
}

// InstallBundledGraphify installs the standalone graphify build: a
// binary bundled next to the running praimate executable wins,
// otherwise the release asset is downloaded natively. Both paths live
// in Run's DownloadAsset dispatch. The method is built without the
// graphifyAssetShipped gate on purpose — the bundled-sidecar copy works
// on every OS, and a missing release asset surfaces as
// ErrNoPrebuiltAsset ("compile from source") rather than a flat
// "unsupported platform".
func InstallBundledGraphify(ctx context.Context, w io.Writer) error {
	binDir, err := PraimateBinDir()
	if err != nil {
		return err
	}
	dest := filepath.Join(binDir, "graphify")
	if runtime.GOOS == "windows" {
		dest += ".exe"
	}
	asset := graphifyAssetName()
	m := Method{
		ID:            "download",
		Label:         "Download PrAImate's bundled graphify (no Python needed)",
		Command:       assetDownloadDisplayCmd(asset, dest),
		DownloadAsset: asset,
		DownloadDest:  dest,
	}
	return Run(ctx, m, w, w)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// ToolMethods returns the install/update methods for a tool. Mirrors
// Methods (the AgentID variant) but with a tool-only catalog.
func ToolMethods(tool ToolID, action Action, current OS) []Method {
	all := allToolMethods(tool, action, current)
	var filtered []Method
	for _, m := range all {
		if !methodAvailable(m, current) {
			continue
		}
		filtered = append(filtered, m)
	}
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

func allToolMethods(tool ToolID, action Action, current OS) []Method {
	switch tool {
	case ToolGraphify:
		// uv tool install graphifyy — pure Python wheels, no native
		// deps, no postinstall scripts (unlike the openclaude case).
		// Installed into a PrAImate-managed prefix via UV_TOOL_DIR /
		// UV_TOOL_BIN_DIR so the user's global PATH stays unaffected
		// and the install is fully isolated from any system Python.
		//
		// --index-url is the supply-chain pin equivalent of pnpm's
		// --registry: defends against a poisoned ~/.config/uv/uv.toml
		// or UV_INDEX_URL env var.
		// VETTED PIN: PrAImate's graphify integration (RAG indexing,
		// `graphify query`, the graphify-out/ layout) is verified against
		// this exact version. Pinning means an upstream release that
		// changes the CLI surface or output layout can't silently break
		// us — installing from the CLIs tab always lands the version we
		// tested, and ResolveGraphify prefers this managed install over
		// whatever's on PATH. The [openai] extra pulls the openai python
		// client so the OpenAI and Local-LLM (OpenAI-compatible) backends
		// work out of the box. Bump deliberately after re-verifying.
		// No shell quoting: Command is split with strings.Fields and
		// exec'd directly (no shell), and the spec has no whitespace.
		pin := "graphifyy[openai]==" + GraphifyPinnedVersion
		cmd := "uv tool install --index-url=https://pypi.org/simple/ " + pin
		if action == ActionUpdate {
			cmd = "uv tool upgrade --index-url=https://pypi.org/simple/ " + pin
		}
		var methods []Method
		// Recommended: PrAImate's bundled standalone build — no Python or
		// uv needed, and it's the exact version we test against.
		if m := graphifyBundledMethod(current); m != nil {
			methods = append(methods, *m)
		}
		methods = append(methods, Method{
			ID:               "uv",
			Label:            "uv tool install (pinned " + GraphifyPinnedVersion + ") into PrAImate-managed prefix",
			Command:          cmd,
			ManagedPrefix:    "graphify",
			ManagedPrefixPkg: pin,
			Recommended:      len(methods) == 0,
			Prereqs:          []string{"uv"},
		})
		return methods
	case ToolGstack:
		// gstack's upstream setup is a bash script that installs skills
		// into whichever supported hosts it detects. The README's broad
		// support list includes Claude Code, Codex, OpenCode and Gemini.
		// DeepSeek-TUI is not a gstack host today, so this stays a skill
		// pack tool rather than a generic runtime helper.
		//
		// GSTACK_SKIP_* prevents setup from making system package-manager
		// changes (fonts/coreutils). PrAImate should install the skill pack,
		// not mutate the host OS behind the user's back.
		return []Method{
			{
				ID:    "bash",
				Label: "git clone + gstack setup into detected supported hosts",
				Command: "git clone --single-branch --depth 1 https://github.com/garrytan/gstack.git <managed>/repo && " +
					"cd <managed>/repo && GSTACK_SKIP_FONTS=1 GSTACK_SKIP_COREUTILS=1 ./setup --host auto --no-team -q",
				ManagedPrefix:    "gstack",
				ManagedPrefixPkg: "https://github.com/garrytan/gstack.git",
				Recommended:      true,
				Prereqs:          []string{"git", "bun"},
			},
		}
	case ToolScrapeGraph:
		// scrapegraphai does not expose a stable console script we can
		// rely on. Install the packages into a PrAImate-owned venv and write
		// our own small `scrapegraph-search` wrapper. The wrapper supports
		// ScrapeGraphAI v2 API mode (SGAI_API_KEY) and OSS SearchGraph mode
		// (local/Ollama-capable via SCRAPEGRAPH_LLM_MODEL).
		const pkgs = "scrapegraphai scrapegraph-py>=2.1.0"
		return []Method{
			{
				ID:               "uv",
				Label:            "uv venv install + Clade scrapegraph-search wrapper",
				Command:          "uv venv <managed>/venv && uv pip install --python <managed>/venv/{bin,Scripts}/python --index-url=https://pypi.org/simple/ scrapegraphai 'scrapegraph-py>=2.1.0'",
				ManagedPrefix:    "scrapegraph",
				ManagedPrefixPkg: pkgs,
				Recommended:      true,
				Prereqs:          []string{"uv"},
			},
		}
	case ToolPraimateCode:
		return praimateCodeMethods(current)
	case ToolPraimateStudio:
		return []Method{
			{
				ID:            "studio",
				Label:         "Install managed PrAImate Studio (Code-OSS + built-in PrAImate extension)",
				Command:       "praimate studio install",
				Recommended:   true,
				ManagedPrefix: "praimate-studio",
			},
		}
	}
	return nil
}

// praimateCodeMethods builds the install method for PrAImate Code: copy
// the binary bundled next to the running praimate executable (the zip /
// tar.gz releases ship it there when built with bun), falling back to a
// native download of the standalone release asset for the host OS/arch
// into <config>/praimate/bin. Run dispatches this to
// InstallPraimateCode, which returns ErrNoPrebuiltAsset when neither
// source exists so callers redirect to "compile from source" instead of
// retrying a download that can never succeed.
func praimateCodeMethods(current OS) []Method {
	binDir, err := PraimateBinDir()
	if err != nil {
		return nil
	}
	dest := filepath.Join(binDir, "praimate-code")
	if current == OSWindows {
		dest += ".exe"
	}
	asset := praimateCodeAssetName()
	m := Method{
		ID:            "download",
		Label:         "Install prebuilt PrAImate Code (bundled copy or release download)",
		Command:       assetDownloadDisplayCmd(asset, dest),
		DownloadAsset: asset,
		DownloadDest:  dest,
		Recommended:   true,
		// Bun-compiled binary: verify it actually runs on this CPU after
		// install, falling back to the -baseline (no-AVX2) build when the
		// default one faults with an illegal instruction. Without this,
		// the install "succeeds" but every probe/launch dies with
		// 0xc000001d and the CLIs tab keeps saying "not installed".
		VerifyRun: true,
	}
	if fb := praimateCodeBaselineAssetName(); fb != "" && fb != asset {
		m.FallbackAsset = fb
	}
	return []Method{m}
}

// ErrNoPrebuiltAsset signals to the caller that the requested binary is
// neither bundled in the install dir NOR published as a release asset
// for the current OS/arch — the right next step is to build from source
// (BuildToolFromSource) instead of retrying the download.
var ErrNoPrebuiltAsset = errors.New("no prebuilt asset available for this OS/arch")

// InstallPraimateCode runs the recommended install for the current OS
// (bundled sidecar copy, else native release download), streaming
// output to w. One-call helper for surfaces that don't drive the
// generic tool-method picker (e.g. the GUI). Returns ErrNoPrebuiltAsset
// when neither a bundled binary nor a published release asset exists
// for the current OS/arch — callers should treat that as "compile from
// source" instead of "retry".
func InstallPraimateCode(ctx context.Context, w io.Writer) error {
	methods := praimateCodeMethods(DetectOS())
	if len(methods) == 0 {
		return fmt.Errorf("locate praimate bin dir failed")
	}
	return Run(ctx, methods[0], w, w)
}

// praimateCodeAssetName is the release asset filename for the host.
// Mirrors the naming the build pipeline uploads. On an amd64 host
// without AVX2 (VMs, older CPUs) the default Bun build crashes with an
// illegal instruction (0xc000001d on Windows), so the "-baseline"
// variant is selected up front.
func praimateCodeAssetName() string {
	return praimateCodeAssetNameFor(runtime.GOOS, runtime.GOARCH, hostNeedsBaselineBuild())
}

// praimateCodeBaselineAssetName is the no-AVX2 variant for the host —
// the post-install verification fallback when the default build turns
// out not to run (CPUID said AVX2 but the hypervisor faults on it).
func praimateCodeBaselineAssetName() string {
	if runtime.GOARCH != "amd64" {
		return "" // baseline variants only exist for x64
	}
	return praimateCodeAssetNameFor(runtime.GOOS, runtime.GOARCH, true)
}

func praimateCodeAssetNameFor(goos, goarch string, baseline bool) string {
	name := fmt.Sprintf("praimate-code-%s-%s", goos, goarch)
	if baseline && goarch == "amd64" {
		name += "-baseline"
	}
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// EnsureUvReady probes uv on PATH. If missing, returns a clear error
// pointing the user at uv's installer — unlike pnpm we do NOT auto-
// install uv (no analog of corepack, and uv has its own supply-chain
// footprint the user should opt into deliberately).
//
// Returns the env additions (empty for uv today — it doesn't need a
// PNPM_HOME-style PATH injection because uv is one statically-linked
// binary) so callers can chain consistently with EnsurePnpmReady.
func EnsureUvReady(ctx context.Context, w io.Writer) ([]string, error) {
	if _, err := exec.LookPath("uv"); err == nil {
		return nil, nil
	}
	hint := "install uv first — it's a single static binary, no Python needed:\n"
	switch runtime.GOOS {
	case "windows":
		hint += "  irm https://astral.sh/uv/install.ps1 | iex\n" +
			"or:\n" +
			"  winget install --id=astral-sh.uv -e"
	case "darwin":
		hint += "  curl -LsSf https://astral.sh/uv/install.sh | sh\n" +
			"or:\n" +
			"  brew install uv"
	default:
		hint += "  curl -LsSf https://astral.sh/uv/install.sh | sh\n" +
			"(installs to ~/.local/bin; ensure that dir is on PATH)"
	}
	return nil, fmt.Errorf("uv not on PATH.\n%s", hint)
}

// ImportManagedToolsToPath prepends every existing managed-tool bin dir
// to PATH so child processes (the launched agent + its tool wrappers)
// can find graphify-and-friends without the user editing their shell
// rc. Mirrors ImportPnpmPathIfPresent. No-op when the tools dir
// doesn't exist yet (first-run, no tools installed).
//
// Safe to call repeatedly; existing entries on PATH are not duplicated.
func ImportManagedToolsToPath() {
	root, err := appdata.Root()
	if err != nil {
		return
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	// Scan the praimate prefix AND the legacy clade prefix: tools
	// installed before the rebrand live under clade/tools and must keep
	// resolving ("graphify installed but not detected").
	toolRoots := []string{filepath.Join(root, "tools")}
	if base, err := os.UserConfigDir(); err == nil {
		toolRoots = append(toolRoots, filepath.Join(base, "clade", "tools"))
	}
	var dirs []string
	for _, toolsDir := range toolRoots {
		entries, err := os.ReadDir(toolsDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(toolsDir, e.Name(), "bin"))
			}
		}
	}
	for _, binDir := range dirs {
		if _, err := os.Stat(binDir); err != nil {
			continue
		}
		path := os.Getenv("PATH")
		cmp := func(a, b string) bool { return a == b }
		if runtime.GOOS == "windows" {
			cmp = strings.EqualFold
		}
		dup := false
		for _, entry := range strings.Split(path, sep) {
			if cmp(strings.TrimRight(entry, `\/`), strings.TrimRight(binDir, `\/`)) {
				dup = true
				break
			}
		}
		if !dup {
			_ = os.Setenv("PATH", binDir+sep+path)
		}
	}
}

// ImportPraimateBinToPath prepends <config>/praimate/bin to PATH so
// managed standalone binaries installed there (praimate-code) are
// discoverable by agent detection and `praimate code`. Called at
// startup alongside ImportManagedToolsToPath.
func ImportPraimateBinToPath() {
	binDir, err := PraimateBinDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(binDir); err != nil {
		return
	}
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	path := os.Getenv("PATH")
	cmp := func(a, b string) bool { return a == b }
	if runtime.GOOS == "windows" {
		cmp = strings.EqualFold
	}
	for _, entry := range strings.Split(path, sep) {
		if cmp(strings.TrimRight(entry, `\/`), strings.TrimRight(binDir, `\/`)) {
			return // already present
		}
	}
	_ = os.Setenv("PATH", binDir+sep+path)
}

// installUvIntoManagedPrefix runs `uv tool install <pkg>` with
// UV_TOOL_DIR / UV_TOOL_BIN_DIR pointed at the PrAImate-managed prefix so
// the binary lands at <prefix>/bin/<m.ManagedPrefix> and uv's package
// state stays in <prefix>/tools/. Mirrors the pnpm managed-prefix
// flow's UX (progress to stdout, success criterion = bin exists).
func installUvIntoManagedPrefix(ctx context.Context, m Method, extraEnv []string, stdout, stderr io.Writer) error {
	prefix, err := ManagedToolPrefix(m.ManagedPrefix)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		return fmt.Errorf("create managed tool prefix %s: %w", prefix, err)
	}
	binDir := filepath.Join(prefix, "bin")
	toolDir := filepath.Join(prefix, "tools")
	for _, d := range []string{binDir, toolDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	fmt.Fprintf(stdout, "→ installing %s into Clade-managed prefix: %s\n", m.ManagedPrefixPkg, prefix)

	// uv reads UV_TOOL_DIR + UV_TOOL_BIN_DIR for the install destination.
	// We pass them via the command env so the user's global uv config is
	// untouched. --index-url is on the command line as a supply-chain
	// pin (also redundantly via UV_INDEX_URL just in case).
	parts := strings.Fields(m.Command)
	if len(parts) == 0 {
		return fmt.Errorf("empty uv command")
	}
	var cap bytes.Buffer
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	hideConsole(cmd)
	env := append(os.Environ(), extraEnv...)
	env = append(env,
		"UV_TOOL_DIR="+toolDir,
		"UV_TOOL_BIN_DIR="+binDir,
		"UV_INDEX_URL=https://pypi.org/simple/",
	)
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(stdout, &cap)
	cmd.Stderr = io.MultiWriter(stderr, &cap)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s into %s: %w\n%s", m.Command, prefix, err, cap.String())
	}

	// Success criterion: the bin exists.
	bin := filepath.Join(binDir, m.ManagedPrefix)
	binPresent := false
	for _, cand := range []string{bin, bin + ".exe", bin + ".cmd", bin + ".bat"} {
		if _, err := os.Stat(cand); err == nil {
			binPresent = true
			bin = cand
			break
		}
	}
	if !binPresent {
		return fmt.Errorf("%s ran but %s binary not found under %s",
			m.Command, m.ManagedPrefix, binDir)
	}
	fmt.Fprintf(stdout, "✓ %s installed; binary at %s\n", m.ManagedPrefix, bin)
	return nil
}

func installGstackIntoManagedPrefix(ctx context.Context, m Method, extraEnv []string, stdout, stderr io.Writer) error {
	prefix, err := ManagedToolPrefix(m.ManagedPrefix)
	if err != nil {
		return err
	}
	repo := filepath.Join(prefix, "repo")
	binDir := filepath.Join(prefix, "bin")
	for _, d := range []string{prefix, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	if _, err := os.Stat(filepath.Join(repo, ".git")); err == nil {
		fmt.Fprintf(stdout, "→ updating gstack checkout: %s\n", repo)
		if err := runToolCommand(ctx, stdout, stderr, "", extraEnv,
			"git", "-C", repo, "pull", "--ff-only"); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "→ cloning gstack into Clade-managed prefix: %s\n", repo)
		if err := os.RemoveAll(repo); err != nil {
			return fmt.Errorf("clean stale gstack checkout %s: %w", repo, err)
		}
		if err := runToolCommand(ctx, stdout, stderr, "", extraEnv,
			"git", "clone", "--single-branch", "--depth", "1", m.ManagedPrefixPkg, repo); err != nil {
			return err
		}
	}

	setupEnv := append([]string{}, extraEnv...)
	setupEnv = append(setupEnv,
		"GSTACK_SKIP_FONTS=1",
		"GSTACK_SKIP_COREUTILS=1",
		"CI=1",
	)
	fmt.Fprintln(stdout, "→ running gstack setup for detected supported hosts...")
	if err := runToolCommand(ctx, stdout, stderr, repo, setupEnv,
		"bash", "./setup", "--host", "auto", "--no-team", "-q"); err != nil {
		return err
	}
	if err := writeGstackWrapper(binDir, repo); err != nil {
		return err
	}
	bin := filepath.Join(binDir, "gstack")
	if runtime.GOOS == "windows" {
		bin += ".cmd"
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("gstack setup ran but wrapper not found at %s: %w", bin, err)
	}
	fmt.Fprintf(stdout, "✓ gstack skills installed; detection wrapper at %s\n", bin)
	return nil
}

func installScrapeGraphIntoManagedVenv(ctx context.Context, m Method, extraEnv []string, stdout, stderr io.Writer) error {
	prefix, err := ManagedToolPrefix(m.ManagedPrefix)
	if err != nil {
		return err
	}
	venv := filepath.Join(prefix, "venv")
	binDir := filepath.Join(prefix, "bin")
	for _, d := range []string{prefix, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
	}

	fmt.Fprintf(stdout, "→ creating ScrapeGraphAI venv: %s\n", venv)
	if err := runToolCommand(ctx, stdout, stderr, "", extraEnv,
		"uv", "venv", venv); err != nil {
		return err
	}
	python := managedVenvPython(venv)
	installEnv := append([]string{}, extraEnv...)
	installEnv = append(installEnv, "UV_INDEX_URL=https://pypi.org/simple/")
	args := []string{
		"pip", "install",
		"--python", python,
		"--index-url=https://pypi.org/simple/",
	}
	args = append(args, strings.Fields(m.ManagedPrefixPkg)...)
	fmt.Fprintln(stdout, "→ installing scrapegraphai + scrapegraph-py into managed venv...")
	if err := runToolCommand(ctx, stdout, stderr, "", installEnv, "uv", args...); err != nil {
		return err
	}
	if err := writeScrapeGraphWrapper(prefix, binDir, python); err != nil {
		return err
	}
	bin := filepath.Join(binDir, "scrapegraph-search")
	if runtime.GOOS == "windows" {
		bin += ".cmd"
	}
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("scrapegraph install ran but wrapper not found at %s: %w", bin, err)
	}
	fmt.Fprintf(stdout, "✓ ScrapeGraphAI installed; binary at %s\n", bin)
	return nil
}

func runToolCommand(ctx context.Context, stdout, stderr io.Writer, dir string, env []string, name string, args ...string) error {
	if name == "git" {
		args = gitutil.DisableSSLVerifyForInternalHostOrOrigin(ctx, dir, args...)
	}
	fmt.Fprintf(stdout, "$ %s\n", strings.Join(append([]string{name}, args...), " "))
	var cap bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsole(cmd)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = io.MultiWriter(stdout, &cap)
	cmd.Stderr = io.MultiWriter(stderr, &cap)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w\n%s", name, err, cap.String())
	}
	return nil
}

func writeGstackWrapper(binDir, repo string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("create gstack bin dir: %w", err)
	}
	version := "gstack installed (managed by Clade)"
	if raw, err := os.ReadFile(filepath.Join(repo, "package.json")); err == nil {
		if i := bytes.Index(raw, []byte(`"version"`)); i >= 0 {
			version = "gstack skill pack (managed by Clade)"
		}
	}
	unix := "#!/usr/bin/env sh\n" +
		"if [ \"${1:-}\" = \"--version\" ]; then\n" +
		"  printf '%s\\n' " + shellQuote(version) + "\n" +
		"  exit 0\n" +
		"fi\n" +
		"printf '%s\\n' 'gstack is installed as AI-agent skills via Clade.'\n" +
		"printf '%s\\n' 'Use the gstack slash commands inside supported hosts: Claude Code, Codex, OpenCode, Gemini CLI.'\n" +
		"printf '%s\\n' 'DeepSeek-TUI is not a documented gstack host.'\n" +
		"printf 'source: %s\\n' " + shellQuote(repo) + "\n"
	if err := os.WriteFile(filepath.Join(binDir, "gstack"), []byte(unix), 0o755); err != nil {
		return fmt.Errorf("write gstack wrapper: %w", err)
	}
	cmd := "@echo off\r\n" +
		"if \"%~1\"==\"--version\" (\r\n" +
		"  echo " + version + "\r\n" +
		"  exit /b 0\r\n" +
		")\r\n" +
		"echo gstack is installed as AI-agent skills via Clade.\r\n" +
		"echo Use the gstack slash commands inside supported hosts: Claude Code, Codex, OpenCode, Gemini CLI.\r\n" +
		"echo DeepSeek-TUI is not a documented gstack host.\r\n" +
		"echo source: " + repo + "\r\n"
	if err := os.WriteFile(filepath.Join(binDir, "gstack.cmd"), []byte(cmd), 0o755); err != nil {
		return fmt.Errorf("write gstack Windows wrapper: %w", err)
	}
	return nil
}

func writeScrapeGraphWrapper(prefix, binDir, python string) error {
	scriptPath := filepath.Join(prefix, "scrapegraph-search.py")
	if err := os.WriteFile(scriptPath, []byte(scrapeGraphSearchPython()), 0o644); err != nil {
		return fmt.Errorf("write scrapegraph-search.py: %w", err)
	}
	unix := "#!/usr/bin/env sh\nexec " + shellQuote(python) + " " + shellQuote(scriptPath) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "scrapegraph-search"), []byte(unix), 0o755); err != nil {
		return fmt.Errorf("write scrapegraph-search wrapper: %w", err)
	}
	cmd := "@echo off\r\n" + strconv.Quote(python) + " " + strconv.Quote(scriptPath) + " %*\r\n"
	if err := os.WriteFile(filepath.Join(binDir, "scrapegraph-search.cmd"), []byte(cmd), 0o755); err != nil {
		return fmt.Errorf("write scrapegraph-search Windows wrapper: %w", err)
	}
	return nil
}

func managedVenvPython(venv string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", "python.exe")
	}
	return filepath.Join(venv, "bin", "python")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func scrapeGraphSearchPython() string {
	return `#!/usr/bin/env python3
import argparse
import json
import os
import sys

VERSION = "scrapegraph-search (Clade wrapper for scrapegraphai + scrapegraph-py)"


def parse_args():
    p = argparse.ArgumentParser(
        prog="scrapegraph-search",
        description="Search/scrape helper backed by ScrapeGraphAI. Prints JSON."
    )
    p.add_argument("query", nargs="*", help="search query or URL task")
    p.add_argument("--prompt", help="explicit extraction prompt; defaults to the query")
    p.add_argument("--mode", choices=("auto", "api", "oss"), default=os.getenv("SCRAPEGRAPH_MODE", "auto"))
    p.add_argument("--results", type=int, default=int(os.getenv("SCRAPEGRAPH_RESULTS", "5")))
    p.add_argument("--version", action="store_true")
    return p.parse_args()


def json_default(value):
    try:
        return vars(value)
    except TypeError:
        return str(value)


def emit(payload):
    print(json.dumps(payload, ensure_ascii=False, indent=2, default=json_default))


def run_api(prompt, results):
    key = os.getenv("SGAI_API_KEY")
    if not key:
        raise RuntimeError("SGAI_API_KEY is required for ScrapeGraphAI API mode")
    try:
        from scrapegraph_py import Client
        client = Client(api_key=key)
        return client.search(query=prompt, num_results=results)
    except ImportError:
        from scrapegraph_py import ScrapeGraphAI
        client = ScrapeGraphAI(api_key=key)
        return client.search(query=prompt, num_results=results)


def run_oss(prompt):
    from scrapegraphai.graphs import SearchGraph

    model = os.getenv("SCRAPEGRAPH_LLM_MODEL", "ollama/llama3.2")
    tokens = int(os.getenv("SCRAPEGRAPH_MODEL_TOKENS", "8192"))
    config = {
        "llm": {
            "model": model,
            "model_tokens": tokens,
        },
        "embeddings": {
            "model": os.getenv("SCRAPEGRAPH_EMBEDDINGS_MODEL", "ollama/nomic-embed-text"),
        },
        "verbose": os.getenv("SCRAPEGRAPH_VERBOSE", "false").lower() == "true",
        "headless": True,
    }
    if os.getenv("SERPER_API_KEY"):
        config["search_engine"] = "serper"
    if os.getenv("SCRAPEGRAPH_SEARCH_ENGINE"):
        config["search_engine"] = os.getenv("SCRAPEGRAPH_SEARCH_ENGINE")
    os.environ.setdefault("SCRAPEGRAPHAI_TELEMETRY_ENABLED", "false")
    graph = SearchGraph(prompt=prompt, config=config)
    return graph.run()


def main():
    args = parse_args()
    if args.version:
        print(VERSION)
        return 0
    query = " ".join(args.query).strip()
    prompt = (args.prompt or query).strip()
    if not prompt:
        print("scrapegraph-search: query or --prompt is required", file=sys.stderr)
        return 2
    try:
        if args.mode == "api" or (args.mode == "auto" and os.getenv("SGAI_API_KEY")):
            emit({"mode": "api", "query": query or prompt, "result": run_api(prompt, args.results)})
        else:
            emit({"mode": "oss", "query": query or prompt, "result": run_oss(prompt)})
    except Exception as exc:
        print("scrapegraph-search failed: " + str(exc), file=sys.stderr)
        if args.mode == "auto" and not os.getenv("SGAI_API_KEY"):
            print("hint: set SGAI_API_KEY for ScrapeGraphAI API mode, or configure OSS mode with SCRAPEGRAPH_LLM_MODEL/SERPER_API_KEY", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
`
}
