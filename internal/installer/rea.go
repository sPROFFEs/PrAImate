package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

const reaVersion = "6.10.1"
const reaPackage = "rea-agents@" + reaVersion

// REA's published families enforce this scope on both tools/list and tools/call.
const reaMCPLauncher = `process.env.REA_MCP_TOOL_FAMILIES = 'artifact,managed,firmware,uefi,android,pcap,flutter'
process.env.REA_MCP_TOOL_CATALOG_PROFILE = 'full'
process.argv = [process.execPath, process.argv[1], 'mcp']
await import('./node_modules/rea-agents/scripts/rea.mjs')
`

var reaInstallMu sync.Mutex

func reaMethod() Method {
	return Method{
		ID: "npm", Label: "Install REA " + reaVersion + " into PrAImate",
		Command:       "npm install --ignore-scripts --engine-strict --registry=https://registry.npmjs.org " + reaPackage,
		ManagedPrefix: string(ToolREA), ManagedPrefixPkg: reaPackage,
		Recommended: true, Prereqs: []string{"node", "npm"},
	}
}

func reaDirectory() (string, error) {
	prefix, err := ManagedToolPrefix(string(ToolREA))
	if err != nil {
		return "", err
	}
	return filepath.Join(prefix, "package"), nil
}

func reaCLI(dir string) string {
	return filepath.Join(dir, "node_modules", "rea-agents", "scripts", "rea.mjs")
}

func findREANode() (string, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return "", fmt.Errorf("REA needs a supported Node.js runtime on PATH: %w", err)
	}
	return filepath.Abs(node)
}

// REAMCPCommand returns the stable stdio launch spec for the managed install.
// An absolute Node path avoids relying on the CLI's own PATH or npm shims.
func REAMCPCommand() (string, string, error) {
	dir, err := reaDirectory()
	if err != nil {
		return "", "", err
	}
	launcher := filepath.Join(dir, "praimate-mcp.mjs")
	if !fileExists(launcher) || !fileExists(reaCLI(dir)) {
		return "", "", fmt.Errorf("REA is not installed in PrAImate")
	}
	node, err := findREANode()
	return node, launcher, err
}

func detectREA(ctx context.Context, tool *Tool) {
	dir, err := reaDirectory()
	if err != nil || !fileExists(reaCLI(dir)) {
		return
	}
	node, _, err := REAMCPCommand()
	if err == nil {
		tool.Version, err = probeToolVersion(ctx, node, reaCLI(dir))
	}
	if err != nil {
		tool.ProbeError = err.Error()
		return
	}
	tool.Binary, tool.Available = reaCLI(dir), true
}

func installREA(ctx context.Context, stdout, stderr io.Writer) error {
	reaInstallMu.Lock()
	defer reaInstallMu.Unlock()
	node, err := findREANode()
	if err != nil {
		return err
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("REA needs npm on PATH: %w", err)
	}
	dir, err := reaDirectory()
	if err != nil {
		return err
	}
	prefix := filepath.Dir(dir)
	if err := os.MkdirAll(prefix, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(prefix, ".install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, "package")
	if err := os.Mkdir(candidate, 0700); err != nil {
		return err
	}
	args := []string{"install", "--global=false", "--ignore-scripts", "--engine-strict", "--no-audit", "--no-fund",
		"--registry=https://registry.npmjs.org", "--prefix", candidate, "--save-exact", reaPackage}
	if runtime.GOOS == "windows" {
		// Launch npm's JS entry point via node so paths are never parsed by cmd.exe.
		npmScript := filepath.Join(filepath.Dir(npm), "node_modules", "npm", "bin", "npm-cli.js")
		if !fileExists(npmScript) {
			npmScript = filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
		}
		if !fileExists(npmScript) {
			return fmt.Errorf("could not locate npm-cli.js next to the Node.js installation")
		}
		npm, args = node, append([]string{npmScript}, args...)
	}
	if err := runToolCommand(ctx, stdout, stderr, candidate, nil, npm, args...); err != nil {
		return err
	}
	version, err := probeToolVersion(ctx, node, reaCLI(candidate))
	if err != nil {
		return fmt.Errorf("verify installed REA: %w", err)
	}
	if version != reaVersion {
		return fmt.Errorf("verify installed REA: expected %s, got %s", reaVersion, version)
	}
	if err := os.WriteFile(filepath.Join(candidate, "praimate-mcp.mjs"), []byte(reaMCPLauncher), 0600); err != nil {
		return err
	}
	// Keep the current installation if npm, the runtime probe or promotion fails.
	backup, err := os.MkdirTemp(prefix, ".previous-")
	if err != nil {
		return err
	}
	previous := filepath.Join(backup, "package")
	if err := os.Rename(dir, previous); err != nil && !os.IsNotExist(err) {
		_ = os.Remove(backup)
		return err
	}
	if err := os.Rename(candidate, dir); err != nil {
		if restoreErr := os.Rename(previous, dir); restoreErr != nil && !os.IsNotExist(restoreErr) {
			return fmt.Errorf("install REA: %w; could not restore previous installation from %s: %v", err, previous, restoreErr)
		}
		_ = os.Remove(backup)
		return err
	}
	_ = os.RemoveAll(backup)
	fmt.Fprintf(stdout, "✓ REA %s installed at %s\n", version, dir)
	return nil
}
