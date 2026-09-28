package main

// `praimate code [args...]` — run the bundled PrAImate Code CLI, our
// version-pinned, rebranded build of OpenCode (MIT). Like `--gui`, the
// heavy artifact ships as a sibling binary (praimate-code) rather than
// inflating the bootstrap binary; this dispatcher locates it and execs
// it with the user's args, forwarding stdio so the interactive coding
// session works normally.
//
// Resolution order:
//  1. praimate-code(.exe) next to this executable (release archives
//     ship them side by side)
//  2. praimate-code on PATH
//  3. <config>/praimate/bin/praimate-code (managed-install location)

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/appdata"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/installer"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func codeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "praimate-code.exe"
	}
	return "praimate-code"
}

// resolveCodeBinary returns the path to praimate-code, or "" if not
// found. Split out for testability.
func resolveCodeBinary(exePath, configDir string) string {
	dataRoot := ""
	if configDir != "" {
		dataRoot = filepath.Join(configDir, "praimate")
	}
	return resolveCodeBinaryAtRoot(exePath, dataRoot)
}

func resolveCodeBinaryAtRoot(exePath, dataRoot string) string {
	name := codeBinaryName()
	candidates := []string{}
	if exePath != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(exePath), name))
	}
	if dataRoot != "" {
		candidates = append(candidates, filepath.Join(dataRoot, "bin", name))
	}
	for _, c := range candidates {
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
			return c
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	return ""
}

// runCode locates praimate-code and runs it with args, returning its
// exit code for main() to pass through.
func runCode(args []string) int {
	exePath, _ := os.Executable()
	exePath, _ = filepath.EvalSymlinks(exePath)
	dataRoot, _ := appdata.Root()

	bin := resolveCodeBinaryAtRoot(exePath, dataRoot)
	if bin == "" {
		fmt.Fprintln(os.Stderr, "praimate: praimate-code not found next to this binary or on PATH.")
		if !offerCodeInstall() {
			return 127
		}
		bin = resolveCodeBinaryAtRoot(exePath, dataRoot)
		if bin == "" {
			fmt.Fprintln(os.Stderr, "praimate: install finished but praimate-code still can't be resolved.")
			return 127
		}
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), prepareCodeMCPEnv()...)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return ee.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "praimate: failed to run praimate-code: %v\n", err)
		return 1
	}
	return 0
}

// offerCodeInstall gives the user a one-command way out of the
// "praimate-code not installed" dead end: on an interactive terminal it
// asks and runs the same install the GUI uses (bundled sidecar copy →
// native release download); non-interactively it prints how to get it.
// Returns true when an install ran and succeeded.
func offerCodeInstall() bool {
	interactive := false
	if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
		interactive = true
	}
	if !interactive {
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "PrAImate Code is our version-pinned build of OpenCode. Get it with:")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "  praimate install-tool praimate-code    # downloads the prebuilt (~150 MB)")
		fmt.Fprintln(os.Stderr)
		fmt.Fprintln(os.Stderr, "or install it from the GUI's CLIs tab, or build from source:")
		fmt.Fprintln(os.Stderr, "  scripts/build-praimate-code.sh   (needs bun)")
		return false
	}
	fmt.Fprint(os.Stderr, "\nPrAImate Code is our version-pinned build of OpenCode.\nDownload the prebuilt now (~150 MB, one time)? [Y/n] ")
	reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	reply = strings.ToLower(strings.TrimSpace(reply))
	if reply != "" && reply != "y" && reply != "yes" {
		fmt.Fprintln(os.Stderr, "skipped — run `praimate install-tool praimate-code` when ready.")
		return false
	}
	if err := installer.InstallPraimateCode(context.Background(), os.Stderr); err != nil {
		if errors.Is(err, installer.ErrNoPrebuiltAsset) {
			fmt.Fprintf(os.Stderr, "\nNo prebuilt exists for %s/%s. Build it from source instead:\n", runtime.GOOS, runtime.GOARCH)
			fmt.Fprintln(os.Stderr, "  scripts/build-praimate-code.sh   (needs git + bun)")
		} else {
			fmt.Fprintf(os.Stderr, "\npraimate: install failed: %v\n", err)
		}
		return false
	}
	return true
}

func prepareCodeMCPEnv() []string {
	cwd, err := os.Getwd()
	if err != nil || cwd == "" {
		return nil
	}
	dbPath, err := store.DefaultDBPath()
	if err != nil {
		return nil
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		return nil
	}
	env, err := c.PrepareEnabledMCPForRun(context.Background(), "praimate-code", cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "praimate: MCP config warning: %v\n", err)
		return nil
	}
	return envMapList(env)
}

func envMapList(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}
