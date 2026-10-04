// Claude Code adapter — drives `claude` non-interactively.
//
// Behaviors:
//
//   SingleShot:
//     claude --print --output-format json [--append-system-prompt ...]
//       message piped on stdin
//
//     The --output-format=json mode emits one JSON object on stdout
//     with the assistant reply and a session_id we can resume from.
//
//   Resume:
//     claude --print --output-format json --resume <session_id>
//       new user message piped on stdin
//
// If `claude` is not on PATH, Available() returns a user-facing error
// pointing at the installer. The SystemPrompt is forwarded via
// --append-system-prompt so it composes on top of the user's CLAUDE.md.

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// isBatchShim reports whether path is a Windows .CMD/.BAT wrapper (how
// pnpm/npm expose node CLIs on Windows). Batch shims run through
// cmd.exe, which truncates the command line at the first newline —
// multi-line argv values cannot survive the trip.
func isBatchShim(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cmd", ".bat":
		return true
	}
	return false
}

// ClaudeAdapter is the production adapter for the Claude Code CLI.
type ClaudeAdapter struct {
	// BinaryPath overrides the discovered binary location. Empty =
	// look it up on PATH. Useful for tests and for users with a
	// non-standard install.
	BinaryPath string

	// name/binary let this same JSON-protocol adapter serve OpenClaude,
	// a Claude Code fork with identical --print/--output-format/--resume
	// flags. Empty defaults to "claude".
	name   string
	binary string
}

// NewClaudeAdapter returns a ready-to-register adapter for `claude`.
func NewClaudeAdapter() *ClaudeAdapter { return &ClaudeAdapter{} }

// NewOpenClaudeAdapter returns an adapter for `openclaude` (Claude Code
// fork, same headless protocol).
func NewOpenClaudeAdapter() *ClaudeAdapter {
	return &ClaudeAdapter{name: "openclaude", binary: "openclaude"}
}

func (a *ClaudeAdapter) Name() string {
	if a.name != "" {
		return a.name
	}
	return "claude"
}

// ManagedSafeMode is true because Claude's non-interactive default denies
// tools that require approval when PrAImate supplies no elevated mode.
func (a *ClaudeAdapter) ManagedSafeMode() bool { return true }

func (a *ClaudeAdapter) binName() string {
	if a.binary != "" {
		return a.binary
	}
	return "claude"
}

func (a *ClaudeAdapter) Available(ctx context.Context) error {
	path, err := a.resolve()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, path, "--version")
	hideConsole(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("claude --version failed: %w", err)
	}
	return nil
}

func (a *ClaudeAdapter) SupportsResume() bool { return true }

func (a *ClaudeAdapter) SingleShot(ctx context.Context, opts SingleShotOpts) (*Reply, error) {
	path, err := a.resolve()
	if err != nil {
		return nil, err
	}
	args, message := a.singleShotArgs(path, "json", opts)
	extra, cleanup, err := approvalArgs(opts.Tools, opts.Approval)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return a.runAt(ctx, path, opts.Cwd, opts.Env, append(args, extra...), message)
}

// singleShotArgs builds the argv (after the binary) + stdin message for
// one fresh turn. format is the --output-format value ("json" for the
// buffered path, "stream-json" for the streaming path).
func (a *ClaudeAdapter) singleShotArgs(path, format string, opts SingleShotOpts) (args []string, message string) {
	args = []string{"--print", "--output-format", format}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	args = append(args, claudeToolsArgs(opts.Tools)...)
	message = opts.Message
	if opts.SystemPrompt != "" {
		if isBatchShim(path) {
			// pnpm/npm install the CLI as a .CMD batch shim on Windows,
			// and cmd.exe truncates the command line at the first
			// newline — a multi-line system prompt via argv silently
			// loses everything after line one. Prepend it to the stdin
			// message instead; stdin is newline-safe.
			message = opts.SystemPrompt + "\n\n" + message
		} else {
			args = append(args, "--append-system-prompt", opts.SystemPrompt)
		}
	}
	return args, message
}

func (a *ClaudeAdapter) Resume(ctx context.Context, sessionID string, opts ResumeOpts) (*Reply, error) {
	if sessionID == "" {
		return nil, errors.New("claude.Resume: empty sessionID")
	}
	args := resumeArgs("json", sessionID, opts)
	extra, cleanup, err := approvalArgs(opts.Tools, opts.Approval)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return a.run(ctx, opts.Cwd, opts.Env, append(args, extra...), opts.Message)
}

// resumeArgs builds the argv for a resumed turn. format as in
// singleShotArgs.
func resumeArgs(format, sessionID string, opts ResumeOpts) []string {
	args := []string{"--print", "--output-format", format, "--resume", sessionID}
	if opts.Model != "" {
		// Re-pin on every turn: --resume continues the conversation but
		// falls back to the default model unless told otherwise.
		args = append(args, "--model", opts.Model)
	}
	// Permission mode is per-invocation too — re-pin it as well.
	return append(args, claudeToolsArgs(opts.Tools)...)
}

// claudeToolsArgs maps the per-chat Tools level onto claude's
// --permission-mode flag. "" = no flag (headless default: tools that
// need approval are denied). "ask" also adds no mode flag — the
// approval prompt tool (see approvalArgs) takes over instead.
func claudeToolsArgs(tools string) []string {
	switch tools {
	case "edits":
		return []string{"--permission-mode", "acceptEdits"}
	case "full":
		return []string{"--permission-mode", "bypassPermissions"}
	}
	return nil
}

// approvalArgs wires the "ask" Tools level: writes a temp --mcp-config
// registering the PrAImate approval shim as MCP server "praimate" and
// points --permission-prompt-tool at its "approve" tool. Claude then
// calls that tool for EVERY permission decision; the shim forwards each
// to the GUI dialog and blocks until the user answers (fail-closed on
// any error). Returns no args when the level isn't "ask" or no shim
// wiring was provided. cleanup removes the temp config after the run.
func approvalArgs(tools string, ap *ApprovalConfig) (args []string, cleanup func(), err error) {
	cleanup = func() {}
	if tools != "ask" || ap == nil || ap.Command == "" {
		return nil, cleanup, nil
	}
	cfg := map[string]any{
		"mcpServers": map[string]any{
			"praimate": map[string]any{
				"command": ap.Command,
				"args":    ap.Args,
			},
		},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, cleanup, fmt.Errorf("approval mcp config: %w", err)
	}
	f, err := os.CreateTemp("", "praimate-approve-*.json")
	if err != nil {
		return nil, cleanup, fmt.Errorf("approval mcp config: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, cleanup, fmt.Errorf("approval mcp config: %w", err)
	}
	_ = f.Close()
	cleanup = func() { _ = os.Remove(f.Name()) }
	return []string{
		"--mcp-config", f.Name(),
		"--permission-prompt-tool", "mcp__praimate__approve",
	}, cleanup, nil
}

// run is the common path: locate the binary, build the command, write
// the user message to stdin, parse the JSON envelope from stdout.
func (a *ClaudeAdapter) run(ctx context.Context, cwd string, env map[string]string, args []string, message string) (*Reply, error) {
	path, err := a.resolve()
	if err != nil {
		return nil, err
	}
	return a.runAt(ctx, path, cwd, env, args, message)
}

// runAt is run with the binary already resolved.
func (a *ClaudeAdapter) runAt(ctx context.Context, path, cwd string, env map[string]string, args []string, message string) (*Reply, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	hideConsole(cmd)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = mergeEnv(cmd.Environ(), env)
	cmd.Stdin = strings.NewReader(message)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("claude exec: %w (stderr=%s)", err, truncate(stderr.String(), 400))
		}
	}
	reply, perr := parseClaudeJSON(stdout.Bytes())
	if perr != nil {
		return nil, fmt.Errorf("parse claude reply: %w (stderr=%s)", perr, truncate(stderr.String(), 400))
	}
	reply.ExitCode = exitCode
	return reply, nil
}

func (a *ClaudeAdapter) resolve() (string, error) {
	if a.BinaryPath != "" {
		return a.BinaryPath, nil
	}
	p, err := exec.LookPath(a.binName())
	if err != nil {
		return "", fmt.Errorf("%s CLI not on PATH; install it from the CLIs tab", a.binName())
	}
	return p, nil
}

// claudeEnvelope mirrors the --output-format=json envelope that
// `claude --print` emits. Fields we don't use are deliberately
// omitted — adding them later is a non-breaking change because we
// decode through a struct, not a generic map.
type claudeEnvelope struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
}

func parseClaudeJSON(body []byte) (*Reply, error) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, errors.New("empty stdout")
	}
	// Newer claude versions emit one JSON object; older versions
	// emit one JSON object per line. Take the LAST non-empty line —
	// it's the terminal envelope either way.
	if i := bytes.LastIndexByte(body, '\n'); i >= 0 {
		body = bytes.TrimSpace(body[i+1:])
	}
	var env claudeEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w (got %q)", err, truncate(string(body), 200))
	}
	return &Reply{
		Text:      strings.TrimRight(env.Result, "\n"),
		SessionID: env.SessionID,
	}, nil
}

func mergeEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return base
	}
	out := append([]string(nil), base...)
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
