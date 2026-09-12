package installer

// User-PATH hydration for desktop launches.
//
// When PrAImate is launched from a Linux .desktop shortcut (or a Wails
// app on macOS, or a Start-menu entry on Windows) the process inherits
// the desktop session's PATH — NOT what the user's shell rc would
// produce. So a CLI installed into ~/.bun/bin / ~/.cargo/bin /
// ~/.local/bin won't resolve via exec.LookPath unless the user has
// logged out + back in. This file fixes that: at startup (and after
// every install) we scan a fixed list of well-known user-level install
// dirs and prepend whichever ones exist on disk.
//
// Each entry is idempotent: re-running is a no-op. Safe to call from
// both the TUI and GUI mains. Pairs with ImportPnpmPathIfPresent,
// ImportManagedToolsToPath, ImportPraimateBinToPath — those handle
// PrAImate-managed prefixes; this one handles dirs the user (or other
// installers) wrote to.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ImportUserBinDirs prepends the well-known per-user CLI install dirs
// to PATH when they exist. Covers bun, deno, cargo, go, npm-global,
// volta, foundry, rye, Homebrew on Apple Silicon, ~/.local/bin, and
// the WinGet / npm-on-Windows dirs.
//
// Repeatable; entries already on PATH are skipped.
func ImportUserBinDirs() {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return
	}
	dirs := candidateUserBinDirs(home)
	prependIfPresent(dirs)
}

// ImportManagedAgentsToPath prepends the executable directories for agents
// installed into PrAImate-owned prefixes. OpenClaude currently uses this
// layout because its dependency tree must be installed locally rather than as
// a global npm package. Keeping the prefix on PATH makes the CLI adapters,
// terminal launcher, and picker detection agree with DetectAgents.
func ImportManagedAgentsToPath() {
	dir, err := ManagedAgentBinDir("openclaude")
	if err != nil {
		return
	}
	prependIfPresent([]string{dir})
}

func candidateUserBinDirs(home string) []string {
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		appd := os.Getenv("APPDATA")
		progFiles := os.Getenv("ProgramFiles")
		dirs := []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".opencode", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".deno", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, "scoop", "shims"),
		}
		if local != "" {
			dirs = append(dirs,
				filepath.Join(local, "Programs", "bun", "bin"),
				filepath.Join(local, "Programs", "nodejs"),
				filepath.Join(local, "Microsoft", "WinGet", "Links"),
				filepath.Join(local, "pnpm"),
				filepath.Join(local, "fnm_multishells"),
				filepath.Join(local, "Volta", "bin"),
			)
		}
		if appd != "" {
			dirs = append(dirs,
				filepath.Join(appd, "npm"),
				filepath.Join(appd, "npm", "bin"),
				filepath.Join(appd, "nvm"),
			)
		}
		if progFiles != "" {
			dirs = append(dirs, filepath.Join(progFiles, "nodejs"))
		}
		return dirs
	case "darwin":
		dirs := []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".deno", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".opencode", "bin"),
			filepath.Join(home, ".claude", "local"),
			filepath.Join(home, ".rye", "shims"),
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".foundry", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".local", "share", "pnpm"),
			filepath.Join(home, ".pnpm"),
			filepath.Join(home, ".asdf", "shims"),
			"/opt/homebrew/bin",
			"/opt/homebrew/sbin",
			"/usr/local/bin",
		}
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(nvmDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(nvmDir, e.Name(), "bin"))
				}
			}
		}
		return dirs
	default:
		dirs := []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, ".bun", "bin"),
			filepath.Join(home, ".deno", "bin"),
			filepath.Join(home, ".cargo", "bin"),
			filepath.Join(home, ".opencode", "bin"),
			filepath.Join(home, ".claude", "local"),
			filepath.Join(home, ".rye", "shims"),
			filepath.Join(home, ".volta", "bin"),
			filepath.Join(home, ".foundry", "bin"),
			filepath.Join(home, "go", "bin"),
			filepath.Join(home, ".npm-global", "bin"),
			filepath.Join(home, ".local", "share", "pnpm"),
			filepath.Join(home, ".pnpm"),
			filepath.Join(home, ".asdf", "shims"),
			filepath.Join(home, ".config", "praimate", "bin"),
			"/home/linuxbrew/.linuxbrew/bin",
			"/home/linuxbrew/.linuxbrew/sbin",
			"/usr/local/bin",
			"/usr/local/sbin",
		}
		nvmDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(nvmDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, filepath.Join(nvmDir, e.Name(), "bin"))
				}
			}
		}
		return dirs
	}
}

func prependIfPresent(dirs []string) {
	sep := ":"
	if runtime.GOOS == "windows" {
		sep = ";"
	}
	eq := func(a, b string) bool { return a == b }
	if runtime.GOOS == "windows" {
		eq = strings.EqualFold
	}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			continue
		}
		path := os.Getenv("PATH")
		dup := false
		for _, entry := range strings.Split(path, sep) {
			if eq(strings.TrimRight(entry, `\/`), strings.TrimRight(d, `\/`)) {
				dup = true
				break
			}
		}
		if !dup {
			_ = os.Setenv("PATH", d+sep+path)
		}
	}
}
