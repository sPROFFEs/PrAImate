// Package backup wires the git-backed cloud sync feature: the
// workspaces root is treated as a git repo whose history mirrors the
// chat + template content across machines.
//
// The implementation deliberately shells out to the `git` binary
// rather than embedding a git library: every machine that would care
// about this feature already has git installed, and the binary stays
// small + the failure modes (auth, conflict resolution, etc.) match
// what the user expects from regular git.
//
// Importantly we never touch GitHub's REST / GraphQL APIs — those
// have aggressive rate limits and require auth tokens. Git over
// HTTPS uses the git smart-HTTP protocol which has different (and
// looser) limits, and works with the user's existing credential
// helper / ssh-agent.
package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/gitutil"
)

// Result captures a single git invocation's outcome. ExitCode is the
// raw OS exit code (0 on success, non-zero otherwise); Stdout / Stderr
// hold the captured output. Err is non-nil whenever ExitCode != 0 or
// the command couldn't be spawned at all (e.g. git missing from PATH).
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// Failed reports whether the invocation should be treated as an
// error. Convenient at call sites that want to fall through to
// "use Stderr in a user-facing message" on failure.
func (r Result) Failed() bool { return r.Err != nil || r.ExitCode != 0 }

// CombinedOutput returns Stdout+Stderr joined, useful for error
// messages where the user wants to see whatever git said.
func (r Result) CombinedOutput() string {
	if r.Stdout == "" {
		return strings.TrimSpace(r.Stderr)
	}
	if r.Stderr == "" {
		return strings.TrimSpace(r.Stdout)
	}
	return strings.TrimSpace(r.Stdout) + "\n" + strings.TrimSpace(r.Stderr)
}

// ErrGitNotInstalled is returned by Run when the `git` binary isn't on
// PATH. Callers should surface a one-liner pointing the user at their
// package manager.
var ErrGitNotInstalled = errors.New("git is not installed or not on PATH")

// Run executes `git <args...>` in dir with the given context. dir
// may be empty (run from the current working directory; rarely what
// you want — most callers pass an explicit repo path).
//
// Returns a Result with the captured stdio + exit code. Failures
// don't return a Go-level error from this function unless git itself
// couldn't be invoked at all; instead the caller inspects Result.Err
// and Result.ExitCode. This keeps call sites simple.
func Run(ctx context.Context, dir string, args ...string) Result {
	if _, err := exec.LookPath("git"); err != nil {
		return Result{Err: ErrGitNotInstalled}
	}
	args = gitutil.DisableSSLVerifyForInternalHostOrOrigin(ctx, dir, args...)
	cmd := exec.CommandContext(ctx, "git", args...)
	hideConsole(cmd)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if err == nil {
		return res
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.ExitCode = exitErr.ExitCode()
		res.Err = exitErr
		return res
	}
	res.ExitCode = -1
	res.Err = err
	return res
}

// UserError converts a Result.Err into a user-facing string. Strips
// the "exit status 1" boilerplate and falls back to Stderr / Stdout.
func UserError(r Result) string {
	if r.Err == nil {
		return ""
	}
	if errors.Is(r.Err, ErrGitNotInstalled) {
		return "git is not installed — install it via your package manager (apt install git / brew install git / winget install Git.Git)"
	}
	if msg := strings.TrimSpace(r.Stderr); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(r.Stdout); msg != "" {
		return msg
	}
	// A non-ExitError means Git never started (for example cmd.Dir does not
	// exist or is inaccessible). Preserve that OS error instead of reducing it
	// to the unactionable "git failed (exit -1)" fallback.
	var exitErr *exec.ExitError
	if !errors.As(r.Err, &exitErr) {
		return r.Err.Error()
	}
	return fmt.Sprintf("git failed (exit %d)", r.ExitCode)
}
