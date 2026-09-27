package core

// execAdapter drives the non-interactive ("headless") mode of CLIs that
// don't share Claude Code's JSON protocol: Codex (`codex exec`),
// OpenCode / PrAImate Code (`opencode run`), Gemini (`gemini -p`), and
// DeepSeek (`deepseek exec`). It runs one process per turn and returns
// the assistant's text.
//
// Resume is intentionally NOT implemented here: each turn is a fresh
// SingleShot. The option-C workflow runner already falls back to
// SingleShot for non-resumable adapters, so multi-step workflows still
// run — each step just starts cold. Most agent workflows are single
// user_message turns, where this is equivalent to a session.
//
// Because these CLIs have no Claude-style --append-system-prompt flag,
// the agent's instructions (SingleShotOpts.SystemPrompt) are prepended
// to the first message so the persona still applies.
//
// The message travels over STDIN, not argv, wherever the CLI supports
// it (codex/opencode/praimate-code/gemini). On Windows, pnpm-installed
// CLIs are .CMD batch shims, and cmd.exe truncates the command line at
// the first newline — a multi-line message passed via argv silently
// loses everything after line one (the "your prompt cuts off" bug).
// Stdin has no such limit and also avoids OS argv length caps.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
)

// buildIn carries the per-turn inputs to an execAdapter's build func.
type buildIn struct {
	Message string
	Model   string
	Tools   string // "", "ask", "edits", "full" — see SingleShotOpts.Tools
	TmpDir  string
}

type execAdapter struct {
	name string
	bin  string
	// managedSafe is true only when empty Tools is enforced as read-only or
	// permission-denying by this adapter's exact argv path.
	managedSafe bool
	// versionArgs probes installation (usually {"--version"}).
	versionArgs []string
	// build returns the argv (after the binary) for a one-shot run of
	// in.Message. If replyFile is non-empty the reply is read from that
	// file (the CLI writes it there); otherwise trimmed stdout is the
	// reply. in.TmpDir is a per-call scratch dir the adapter may use.
	// in.Model is the per-turn model override ("" = CLI default) and
	// in.Tools the permission level — CLIs without matching flags
	// ignore them.
	build func(in buildIn) (args []string, replyFile string)
	// stdinMsg pipes the message to the child's stdin instead of argv;
	// build then must NOT embed the message in args. Required for
	// newline-safety through Windows .CMD shims (see file comment).
	stdinMsg bool
	// extraDirs are checked (before PATH) for the binary — used by
	// praimate-code, whose standard install lands in the managed bin
	// dir rather than on PATH.
	extraDirs []string
}

func (a *execAdapter) ManagedSafeMode() bool { return a.managedSafe }

// resolveBin locates the adapter's binary: extraDirs first, then PATH.
func (a *execAdapter) resolveBin() (string, error) {
	name := a.bin
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, dir := range a.extraDirs {
		candidate := filepath.Join(dir, name)
		if fi, err := os.Stat(candidate); err == nil && !fi.IsDir() {
			return candidate, nil
		}
	}
	return exec.LookPath(a.bin)
}

// praimateManagedBinDir mirrors installer.PraimateBinDir without the
// import (core must not grow an installer dependency): the managed
// standalone dir is <user config dir>/praimate/bin.
func praimateManagedBinDir() string {
	base, err := appdata.Root()
	if err != nil {
		return ""
	}
	return filepath.Join(base, "bin")
}

func (a *execAdapter) Name() string { return a.name }

func (a *execAdapter) SupportsResume() bool {
	return isOpenCodeLikeAdapter(a.name) || a.name == "codex"
}

func (a *execAdapter) Resume(ctx context.Context, sessionID string, opts ResumeOpts) (*Reply, error) {
	if isOpenCodeLikeAdapter(a.name) {
		return a.openCodeResumeStream(ctx, sessionID, opts, nil)
	}
	if a.name == "codex" {
		return a.codexResumeStream(ctx, sessionID, opts, nil)
	}
	return nil, fmt.Errorf("%s adapter does not support resume", a.name)
}

func (a *execAdapter) Available(ctx context.Context) error {
	path, err := a.resolveBin()
	if err != nil {
		return fmt.Errorf("%s CLI not on PATH; install it from the CLIs tab", a.bin)
	}
	args := a.versionArgs
	if len(args) == 0 {
		args = []string{"--version"}
	}
	probe := exec.CommandContext(ctx, path, args...)
	hideConsole(probe)
	if err := probe.Run(); err != nil {
		return fmt.Errorf("%s %s failed: %w", a.bin, strings.Join(args, " "), err)
	}
	return nil
}

func (a *execAdapter) SingleShot(ctx context.Context, opts SingleShotOpts) (*Reply, error) {
	if isOpenCodeLikeAdapter(a.name) {
		return a.openCodeSingleShotStream(ctx, opts, nil)
	}

	path, err := a.resolveBin()
	if err != nil {
		return nil, fmt.Errorf("%s CLI not on PATH", a.bin)
	}

	msg := opts.Message
	if s := strings.TrimSpace(opts.SystemPrompt); s != "" {
		msg = s + "\n\n" + msg
	}

	tmpDir, err := os.MkdirTemp("", "praimate-"+a.name+"-")
	if err != nil {
		return nil, fmt.Errorf("%s: scratch dir: %w", a.name, err)
	}
	defer os.RemoveAll(tmpDir)

	args, replyFile := a.build(buildIn{Message: msg, Model: opts.Model, Tools: opts.Tools, TmpDir: tmpDir})
	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if a.stdinMsg {
		cmd.Stdin = strings.NewReader(msg)
	}
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = mergeEnv(os.Environ(), opts.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if runErr := cmd.Run(); runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("%s exec: %w (stderr=%s)", a.name, runErr, truncate(stderr.String(), 400))
		}
	}

	var text string
	if replyFile != "" {
		b, rerr := os.ReadFile(replyFile)
		if rerr != nil {
			// Fall back to stdout if the CLI didn't write the file
			// (e.g. it errored before producing a final message).
			text = stdout.String()
		} else {
			text = string(b)
		}
	} else {
		text = stdout.String()
	}
	text = strings.TrimRight(text, "\n")
	if text == "" && exitCode != 0 {
		// Surface the CLI's own error so the run result is diagnosable.
		text = strings.TrimSpace(stderr.String())
	}
	return &Reply{Text: text, ExitCode: exitCode}, nil
}

// --- Constructors for each CLI -----------------------------------------

// NewCodexAdapter drives `codex exec`. --output-last-message writes only
// the final assistant message to a file, which we read back cleanly.
// --skip-git-repo-check lets it run outside a git repo. The prompt arg
// is "-" so codex reads the (possibly multi-line) prompt from stdin.
func NewCodexAdapter() *execAdapter {
	return &execAdapter{
		name: "codex", bin: "codex", stdinMsg: true, managedSafe: true,
		build: func(in buildIn) ([]string, string) {
			out := filepath.Join(in.TmpDir, "reply.txt")
			args := []string{"exec", "--skip-git-repo-check", "--output-last-message", out}
			if in.Model != "" {
				args = append(args, "-m", in.Model)
			}
			args = append(args, codexPermissionArgs(in.Tools, false)...)
			return append(args, "-"), out
		},
	}
}

func codexPermissionArgs(tools string, resume bool) []string {
	switch tools {
	case "edits":
		if resume {
			return []string{"-c", `sandbox_mode="workspace-write"`}
		}
		return []string{"--sandbox", "workspace-write"}
	case "full":
		return []string{"--dangerously-bypass-approvals-and-sandbox"}
	default:
		// Codex's own default can be workspace-write depending on config/version.
		// PrAImate Safe/Ask must stay read-only unless the user selects Edits/Full.
		if resume {
			return []string{"-c", `sandbox_mode="read-only"`}
		}
		return []string{"--sandbox", "read-only"}
	}
}

// NewOpenCodeAdapter drives `opencode run` with the message piped on
// stdin (documented: `echo "..." | opencode run`). Also the basis for
// praimate-code.
func NewOpenCodeAdapter() *execAdapter {
	return &execAdapter{
		name: "opencode", bin: "opencode", stdinMsg: true, managedSafe: true,
		build: func(in buildIn) ([]string, string) {
			args := []string{"run"}
			if in.Model != "" {
				// opencode expects provider/model, e.g.
				// anthropic/claude-sonnet-4-5.
				args = append(args, "--model", in.Model)
			}
			return args, ""
		},
	}
}

// NewPraimateCodeAdapter drives our bundled OpenCode build, same `run`
// interface.
func NewPraimateCodeAdapter() *execAdapter {
	return &execAdapter{
		name: "praimate-code", bin: "praimate-code", stdinMsg: true, managedSafe: true,
		extraDirs: []string{praimateManagedBinDir()},
		build: func(in buildIn) ([]string, string) {
			args := []string{"run"}
			if in.Model != "" {
				args = append(args, "--model", in.Model)
			}
			return args, ""
		},
	}
}

// RegisterAllCLIAdapters installs every built-in CLI adapter into the
// global registry. Idempotent. Called once at process start by both the
// TUI and the GUI so workflows run on whichever CLI an agent declares.
func RegisterAllCLIAdapters() {
	RegisterCLIAdapter(NewClaudeAdapter())
	RegisterCLIAdapter(NewOpenClaudeAdapter())
	RegisterCLIAdapter(NewCodexAdapter())
	RegisterCLIAdapter(NewOpenCodeAdapter())
	RegisterCLIAdapter(NewPraimateCodeAdapter())
	RegisterCLIAdapter(NewPraimateCLIAdapter())
}
