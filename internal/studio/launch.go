package studio

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
)

var desktopServers struct {
	sync.Mutex
	core            *core.Core
	server          *Server
	endpoint, token string
	window          *DesktopWindowHooks
}

// SetDesktopWindowHooks attaches the Wails window controller to the shared
// authenticated Studio server. It is intentionally separate from Core.
func SetDesktopWindowHooks(c *core.Core, hooks *DesktopWindowHooks) {
	desktopServers.Lock()
	defer desktopServers.Unlock()
	desktopServers.window = hooks
	if desktopServers.server != nil && desktopServers.core == c {
		desktopServers.server.setDesktopWindowHooks(hooks)
	}
}

func desktopEndpoint(c *core.Core) (string, string, error) {
	desktopServers.Lock()
	defer desktopServers.Unlock()
	if desktopServers.server != nil && desktopServers.core == c {
		return desktopServers.endpoint, desktopServers.token, nil
	}
	srv := NewServer(c)
	srv.setDesktopWindowHooks(desktopServers.window)
	endpoint, token, err := srv.StartLocal()
	if err != nil {
		return "", "", err
	}
	if desktopServers.server != nil {
		_ = desktopServers.server.Close()
	}
	desktopServers.core, desktopServers.server, desktopServers.endpoint, desktopServers.token = c, srv, endpoint, token
	return endpoint, token, nil
}

func backendExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "praimate"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if strings.EqualFold(filepath.Base(exe), name) {
		return exe, nil
	}
	sibling := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
		return sibling, nil
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("PrAImate backend executable is missing; expected %s", sibling)
}

func studioEnvironment(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		replaced := false
		for candidate := range overrides {
			if strings.EqualFold(key, candidate) {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, item)
		}
	}
	for key, value := range overrides {
		out = append(out, key+"="+value)
	}
	return out
}

func launchConfiguration(opts LaunchOptions, workspace string) string {
	cli, tools := opts.CLI, opts.Tools
	if cli == "" {
		cli = "praimate-code"
	}
	if tools == "" {
		tools = "safe"
	}
	body, _ := json.Marshal(sessionConfig{CLI: cli, Tools: tools, Model: opts.Model, AgentID: opts.AgentID,
		Workspace: workspace, LocalEndpoint: opts.LocalEndpoint, LocalModel: opts.LocalModel, MCPServers: opts.MCPServers})
	return string(body)
}

func extensionFiles() map[string]string {
	return map[string]string{"package.json": ExtensionPackageJSON, "extension.js": ExtensionJS, "rpc.js": ExtensionRPCJS, "terminal.js": ExtensionTerminalJS,
		"resources/chat.html": ExtensionChatHTML, "resources/monke.svg": ExtensionSVG}
}
func extensionCurrent(dir string) bool {
	for name, want := range extensionFiles() {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			return false
		}
	}
	return true
}
func provisionExtension(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "resources"), 0755); err != nil {
		return err
	}
	for name, body := range extensionFiles() {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
			return err
		}
	}
	return nil
}

// Older Studio builds bundled a second copy inside Code-OSS. Upgrade it too:
// an existing extension host may resolve that copy instead of the user copy.
// Never modify extensions in a system-installed editor.
func extensionLocations() ([]string, error) {
	ext, err := ExtensionsDir()
	if err != nil {
		return nil, err
	}
	app, err := AppDir()
	if err != nil {
		return nil, err
	}
	dirs := []string{filepath.Join(ext, "praimate")}
	roots := []string{app}
	entries, _ := os.ReadDir(app)
	for _, entry := range entries {
		if entry.IsDir() {
			roots = append(roots, filepath.Join(app, entry.Name()))
		}
	}
	for _, root := range roots {
		dir := filepath.Join(root, "resources", "app", "extensions", "praimate")
		body, err := os.ReadFile(filepath.Join(dir, "package.json"))
		var manifest struct{ Name string }
		if err == nil && json.Unmarshal(body, &manifest) == nil && manifest.Name == "praimate-studio" {
			dirs = append(dirs, dir)
		}
	}
	return dirs, nil
}

func provisionExtensions() error {
	dirs, err := extensionLocations()
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := provisionExtension(dir); err != nil {
			return err
		}
	}
	return nil
}

// Code-OSS keeps its first process environment across launches, including after
// Desktop restarts. The private, atomically replaced descriptor lets both old
// and new windows discover the current backend without logging credentials.
func publishConnection(endpoint, token, backend, config string) error {
	dirs, err := extensionLocations()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"endpoint": endpoint, "token": token, "backend": backend,
		"config": json.RawMessage(config), "launchId": fmt.Sprint(time.Now().UnixNano())})
	if err != nil {
		return err
	}
	for _, dir := range dirs {
		if err := writeConnection(dir, body); err != nil {
			return err
		}
	}
	return nil
}

func writeConnection(dir string, body []byte) error {
	f, err := os.CreateTemp(dir, ".connection-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "connection.json"))
}
