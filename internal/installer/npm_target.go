package installer

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Keep an npm update bound to the installation actually found by detection.
// In particular, a CLI in an older Node version must not be "updated" into
// the active Node version while the original executable remains unchanged.
func alignNpmTarget(cmd *exec.Cmd, method Method, existing string, output io.Writer) {
	if existing == "" || method.Shell != ShellDirect || !strings.HasPrefix(method.Command, "npm ") {
		return
	}
	global := false
	for _, arg := range cmd.Args {
		if arg == "-g" || arg == "--global" {
			global = true
		}
	}
	if !global {
		return
	}
	binary, err := exec.LookPath(existing)
	if err != nil {
		return
	} // First installation has no target to preserve.
	dir := filepath.Dir(binary)
	if stable, err := filepath.EvalSymlinks(dir); err == nil {
		dir = stable
	}
	node, err := exec.LookPath(filepath.Join(dir, "node"))
	if err != nil {
		return
	}
	prefix := filepath.Dir(dir)
	script := filepath.Join(prefix, "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if runtime.GOOS == "windows" {
		prefix = dir
		script = filepath.Join(prefix, "node_modules", "npm", "bin", "npm-cli.js")
	}
	if info, err := os.Stat(script); err != nil || !info.Mode().IsRegular() {
		return
	}
	args := append([]string{node, script}, cmd.Args[1:]...)
	args = append(args, "--prefix", prefix)
	cmd.Path, cmd.Args = node, args
	cmd.Err = nil // The original npm lookup may have failed in the other runtime.
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	path := os.Getenv("PATH")
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if found && (key == "PATH" || runtime.GOOS == "windows" && strings.EqualFold(key, "PATH")) {
			path = value
			continue
		}
		filtered = append(filtered, entry)
	}
	cmd.Env = append(filtered, "PATH="+dir+string(os.PathListSeparator)+path)
	if output != nil {
		fmt.Fprintf(output, "Updating the detected CLI using Node %s and npm prefix %s\n", node, prefix)
	}
}
