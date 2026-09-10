package main

// Resolution from a PrAImate CLI name to the actual interactive
// command to spawn in a PTY. Versioned native discovery is capability-gated;
// legacy persona context is an exclusively owned, temporary project file.

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

// terminalCommand maps a PrAImate CLI id to the binary + interactive
// args to launch. We deliberately launch the CLI in its normal
// interactive mode — the whole point is the user gets the real tool.
// model, when non-empty, is passed with the CLI's own model flag
// when non-empty, is passed with the CLI's own model flag.
func terminalCommand(cli, model string) (name string, args []string, err error) {
	switch cli {
	case "claude", "openclaude":
		if model != "" {
			args = []string{"--model", model}
		}
		return cli, args, nil
	case "codex":
		if model != "" {
			args = []string{"-m", model}
		}
		return "codex", args, nil
	case "opencode", "praimate-code":
		if model != "" {
			args = []string{"--model", model}
		}
		return cli, args, nil
	case "":
		return "", nil, fmt.Errorf("no CLI selected for the terminal")
	default:
		return "", nil, fmt.Errorf("unknown CLI %q", cli)
	}
}

// prepareLegacyTerminalContext preserves the old native persona convention
// without refreshing/overwriting user files or another live session's context.
// Creation is exclusive; cleanup uses the open root and removes only the same
// regular file with the exact original contents. No native read is asserted.
func prepareLegacyTerminalContext(cwd, cli string, agent *core.Agent, prefix string) (func(), error) {
	var body strings.Builder
	if agent != nil {
		body.WriteString(core.AgentSystemPrompt(agent))
	}
	if prefix != "" {
		body.WriteString("\n\n" + prefix)
	}
	if strings.TrimSpace(body.String()) == "" {
		return nil, nil
	}
	file := "AGENTS.md"
	if cli == "claude" || cli == "openclaude" {
		file = "CLAUDE.md"
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, err
	}
	f, err := root.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("terminal context: cannot exclusively create %s; preserve the existing project instructions or close the other agent terminal, then use a clean project or a Chat/Studio session: %w", file, err)
	}
	content := []byte("<!-- praimate:temporary-agent-context -->\n" + body.String() + "\n")
	_, writeErr := f.Write(content)
	info, statErr := f.Stat()
	closeErr := f.Close()
	if writeErr != nil || statErr != nil || closeErr != nil {
		// No recursive deletion; this file was created exclusively above.
		_ = root.Remove(file)
		root.Close()
		return nil, fmt.Errorf("terminal context write failed: %v %v %v", writeErr, statErr, closeErr)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			defer root.Close()
			named, err := root.Lstat(file)
			if err != nil || !named.Mode().IsRegular() || !os.SameFile(info, named) {
				return
			}
			current, err := root.ReadFile(file)
			if err == nil && bytes.Equal(current, content) {
				_ = root.Remove(file)
			}
		})
	}, nil
}

// terminalResumeCommand reopens the most recent native interactive session in
// cwd for CLIs that expose a deterministic non-picker flag. The caller still
// supplies cwd through Cmd.Dir, so "most recent" is folder-scoped.
func terminalResumeCommand(cli, model string) (name string, args []string, supported bool, err error) {
	name, args, err = terminalCommand(cli, model)
	if err != nil {
		return "", nil, false, err
	}
	switch cli {
	case "claude", "openclaude", "opencode", "praimate-code":
		return name, append(args, "--continue"), true, nil
	case "codex":
		args = []string{"resume", "--last"}
		if model != "" {
			args = append(args, "--model", model)
		}
		return name, args, true, nil
	default:
		return name, args, false, nil
	}
}

// appendEnvMap folds generated launch secrets into an env overlay,
// replacing any existing value for the same key.
func appendEnvMap(env []string, extra map[string]string) []string {
	if len(extra) == 0 {
		return env
	}
	index := map[string]int{}
	for i, kv := range env {
		if key, _, ok := strings.Cut(kv, "="); ok {
			index[key] = i
		}
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		kv := key + "=" + extra[key]
		if i, ok := index[key]; ok {
			env[i] = kv
			continue
		}
		index[key] = len(env)
		env = append(env, kv)
	}
	return env
}
