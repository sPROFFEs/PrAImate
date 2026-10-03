package gitutil

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// internalGitHost is empty in the public GitHub build — GitHub uses a
// public CA-signed cert, so there's nothing to bypass. Downstream forks
// that host on a self-signed internal Gitea/GitLab can set this to their
// forge host (or export PRAIMATE_INTERNAL_GIT_HOST) and rebuild.
var internalGitHost = envOrDefault("PRAIMATE_INTERNAL_GIT_HOST", "git.jtsec.local")

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

// DisableSSLVerifyForInternalHost prepends Git's per-command SSL bypass
// only when the command targets the internal forge host.
func DisableSSLVerifyForInternalHost(args ...string) []string {
	if !ContainsInternalHost(args...) {
		return cloneArgs(args)
	}
	return withSSLVerifyDisabled(args)
}

// DisableSSLVerifyForInternalHostOrOrigin also covers commands like
// `git fetch origin` where an internal-host URL lives in the repo config.
func DisableSSLVerifyForInternalHostOrOrigin(ctx context.Context, dir string, args ...string) []string {
	if ContainsInternalHost(args...) || originTargetsInternalHost(ctx, dir, args) {
		return withSSLVerifyDisabled(args)
	}
	return cloneArgs(args)
}

func ContainsInternalHost(values ...string) bool {
	if internalGitHost == "" {
		return false
	}
	needle := strings.ToLower(internalGitHost)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func withSSLVerifyDisabled(args []string) []string {
	out := make([]string, 0, len(args)+2)
	out = append(out, "-c", "http.sslVerify=false")
	out = append(out, args...)
	return out
}

func cloneArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	return out
}

func originTargetsInternalHost(ctx context.Context, dir string, args []string) bool {
	workdir := gitWorkDir(dir, args)
	if strings.TrimSpace(workdir) == "" {
		return false
	}
	out, err := originCommand(ctx, workdir).Output()
	if err != nil {
		return false
	}
	return ContainsInternalHost(string(out))
}

func originCommand(ctx context.Context, workdir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", "-C", workdir, "config", "--get", "remote.origin.url")
	hideConsole(cmd)
	return cmd
}

func gitWorkDir(dir string, args []string) string {
	if strings.TrimSpace(dir) != "" {
		return dir
	}
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "-C" {
			return args[i+1]
		}
	}
	return ""
}
