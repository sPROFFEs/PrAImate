package core

import (
	"fmt"
	"os/exec"
	"strings"
)

// InteractiveCLICommand is shared by Desktop and Studio's native terminals.
// These are interactive CLI arguments, not the headless chat adapter protocol.
// No tool-permission, persona, skills or MCP settings are implied by this helper.
func InteractiveCLICommand(cli, model string) (string, []string, error) {
	flag := "--model"
	switch cli {
	case "claude", "openclaude", "opencode", "praimate-code", "praimate-cli", "copilot", "antigravity":
	case "codex":
		flag = "-m"
	case "":
		return "", nil, fmt.Errorf("no CLI selected for the terminal")
	default:
		return "", nil, fmt.Errorf("unknown CLI %q", cli)
	}
	if strings.ContainsAny(model, "\x00\r\n") || strings.HasPrefix(model, "-") {
		return "", nil, fmt.Errorf("model must be a model identifier, not a flag or multiline command")
	}
	args := []string{}
	if model != "" {
		args = append(args, flag, model)
	}
	binary := cli
	if cli == "antigravity" {
		binary = "agy"
	}
	return binary, args, nil
}

// ResolveInteractiveCLIBinary locates the same executable as the production
// adapter, including managed PrAImate Code installations outside PATH. Unlike
// a headless --version probe this also accepts Windows npm/pnpm batch shims;
// the terminal frontend is responsible for their platform-specific execution.
func ResolveInteractiveCLIBinary(cli string) (string, error) {
	if _, _, err := InteractiveCLICommand(cli, ""); err != nil {
		return "", err
	}
	adapter, _ := GetCLIAdapter(cli)
	switch adapter := adapter.(type) {
	case *ClaudeAdapter:
		return adapter.resolve()
	case *execAdapter:
		return adapter.resolveBin()
	case *additionalCLIAdapter:
		return adapter.resolveBin()
	}
	if cli == "praimate-code" {
		return NewPraimateCodeAdapter().resolveBin()
	}
	if cli == "praimate-cli" {
		return resolvePraimateCLIBinary(nativeExecutableDir(), praimateManagedBinDir())
	}
	if cli == "antigravity" {
		return NewAntigravityAdapter().resolveBin()
	}
	return exec.LookPath(cli)
}

// The CLI shipped next to Desktop must win over a separately installed copy:
// Studio and Desktop share the core version packaged with that Desktop build.
func resolvePraimateCLIBinary(executableDir, managedDir string) (string, error) {
	return (&execAdapter{bin: "praimate-cli", extraDirs: []string{executableDir, managedDir}}).resolveBin()
}
