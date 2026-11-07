package serverboot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/store"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Status struct {
	Supported bool   `json:"supported"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	Running   bool   `json:"running"`
	BootReady bool   `json:"bootReady"`
	Tunnel    bool   `json:"tunnel"`
	Platform  string `json:"platform"`
	Name      string `json:"name"`
	Binary    string `json:"binary"`
	Note      string `json:"note"`
	Error     string `json:"error,omitempty"`
}
type Manager struct {
	mu      sync.Mutex
	c       Configuration
	run     func(context.Context, []string) (string, error)
	elevate func(context.Context, Configuration, string, string) error
	verify  func(string) error
}

func New(database string) (*Manager, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if resolved, e := filepath.EvalSymlinks(binary); e == nil {
		binary = resolved
	}
	database, err = filepath.Abs(database)
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	c := Configuration{Database: database, Binary: binary, Home: home, User: u.Username, UID: u.Uid, Root: database + ".startup", ProfileHome: os.Getenv("PRAIMATE_HOME")}
	c.Environment = map[string]string{}
	for _, key := range profileEnvironment {
		if value := os.Getenv(key); value != "" {
			if absolute(runtime.GOOS, value) {
				c.Environment[key] = value
			}
		}
	}
	if c.ProfileHome != "" {
		c.ProfileHome, err = filepath.Abs(c.ProfileHome)
		if err != nil {
			return nil, err
		}
	}
	return &Manager{c: c, run: runCommand, elevate: elevate, verify: func(password string) error { return store.VerifyPassword(c.Database, password) }}, nil
}

func (m *Manager) operationLock() (*os.File, error) {
	if err := secureDirectory(m.c.Root); err != nil {
		return nil, err
	}
	return remote.LockProfile(filepath.Join(m.c.Root, "operation"))
}
func ReadConfiguration(path string) (Configuration, error) {
	var c Configuration
	body, err := readPrivate(path, 65536)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(body, &c)
	if err != nil {
		return c, errors.New("Invalid startup configuration")
	}
	if err = c.validate(runtime.GOOS); err != nil {
		return c, err
	}
	if filepath.Clean(path) != filepath.Join(c.Root, "config.json") || c.Root != c.Database+".startup" {
		return c, errors.New("Startup configuration does not match its profile")
	}
	return c, nil
}
func (m *Manager) configuration() (Configuration, bool, error) {
	c, err := ReadConfiguration(filepath.Join(m.c.Root, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m.c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if c.Database != m.c.Database || c.UID != m.c.UID {
		return c, false, errors.New("Startup belongs to another profile or OS account")
	}
	return c, true, nil
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, installed, err := m.configuration()
	s := Status{Supported: runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows", Platform: runtime.GOOS, Name: m.c.Name()}
	if err != nil {
		s.Error = err.Error()
		return s, nil
	}
	if !installed {
		return s, nil
	}
	s.Installed = true
	s.Binary = c.Binary
	s.Tunnel = c.Tunnel
	s.Enabled, s.Running, s.BootReady, s.Note, err = m.nativeStatus(ctx, c)
	if c.Binary != m.c.Binary {
		s.Note += " This service uses another executable location; update startup configuration to use the current installation."
	}
	if err != nil {
		s.Error = err.Error()
	}
	if _, err := os.Stat(c.Binary); err != nil {
		s.BootReady = false
		s.Error = "Installed startup executable is missing; update startup configuration from the installed Server app"
	}
	if _, err := os.Stat(filepath.Join(c.Root, "unlock")); err != nil {
		s.BootReady = false
		s.Error = "Startup unlock credential is missing; update startup configuration"
	}
	return s, nil
}
func (m *Manager) Install(ctx context.Context, password, windowsPassword string, tunnel bool) (result error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if password == "" || len(password) > 4096 {
		return errors.New("Enter the current database password")
	}
	if runtime.GOOS == "windows" && windowsPassword == "" {
		return errors.New("Windows account password is required for unattended startup with network access; a PIN cannot be used")
	}
	c := m.c
	c.Tunnel = tunnel
	configHome, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	p, err := makePlan(runtime.GOOS, c, configHome)
	if err != nil {
		return err
	}
	if _, installed, err := m.configuration(); err != nil {
		return err
	} else if !installed {
		if _, err := os.Lstat(p.Path); err == nil {
			return errors.New("An unmanaged startup definition already exists; inspect it before installing")
		}
	}
	if err = secureDirectory(c.Root); err != nil {
		return err
	}
	lease, err := m.operationLock()
	if err != nil {
		return fmt.Errorf("Another startup operation is in progress or unavailable: %w", err)
	}
	defer lease.Close()
	if m.verify != nil {
		if err := m.verify(password); err != nil {
			return errors.New("The database password changed or could not be verified; enter its current password")
		}
	}
	paths := []string{filepath.Join(c.Root, "unlock"), filepath.Join(c.Root, "config.json")}
	if runtime.GOOS != "darwin" {
		paths = append(paths, p.Path)
	}
	type previous struct {
		path   string
		body   []byte
		mode   os.FileMode
		exists bool
	}
	backups := []previous{}
	for _, path := range paths {
		old := previous{path: path}
		if info, e := os.Lstat(path); e == nil {
			if !info.Mode().IsRegular() || info.Size() > 65536 {
				return errors.New("Refusing to replace an unsafe startup file")
			}
			old.body, err = os.ReadFile(path)
			if err != nil {
				return err
			}
			old.mode = info.Mode().Perm()
			old.exists = true
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		backups = append(backups, old)
	}
	defer func() {
		for _, old := range backups {
			if result != nil {
				if old.exists {
					if err := atomicWrite(old.path, old.body, old.mode); err != nil {
						result = errors.Join(result, fmt.Errorf("Restore previous startup file: %w", err))
					}
				} else {
					_ = os.Remove(old.path)
				}
			}
			clear(old.body)
		}
		if result != nil && runtime.GOOS == "linux" {
			_, _ = m.run(ctx, []string{"systemctl", "--user", "daemon-reload"})
		}
	}()
	credential, err := protect([]byte(password))
	if err != nil {
		return errors.New("Cannot protect the service unlock credential")
	}
	defer clear(credential)
	if err = privateWrite(filepath.Join(c.Root, "unlock"), credential); err != nil {
		return err
	}
	body, _ := json.Marshal(c)
	if err = privateWrite(filepath.Join(c.Root, "config.json"), body); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err = atomicWrite(p.Path, []byte(p.Content), 0644); err != nil {
			return err
		}
		return m.linuxControl(ctx, c, "install")
	}
	if runtime.GOOS == "windows" {
		if err = privateWrite(p.Path, []byte(p.Content)); err != nil {
			return err
		}
	}
	return m.elevate(ctx, c, "install", windowsPassword)
}

func atomicWrite(path string, body []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".startup-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func excludeFromGit(directory string) error {
	path := filepath.Join(directory, ".gitignore")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("Startup backup exclusion must be a regular file")
		}
		if info.Size() == 2 {
			if body, err := os.ReadFile(path); err == nil && string(body) == "*\n" {
				return nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, []byte("*\n"), 0600)
}
func (m *Manager) Control(ctx context.Context, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !map[string]bool{"enable": true, "disable": true, "start": true, "stop": true, "restart": true, "uninstall": true}[action] {
		return errors.New("Unknown startup service operation")
	}
	lease, err := m.operationLock()
	if err != nil {
		return fmt.Errorf("Another startup operation is in progress or unavailable: %w", err)
	}
	defer lease.Close()
	c, installed, err := m.configuration()
	if err != nil {
		return err
	}
	if !installed {
		return errors.New("Install startup for this profile first")
	}
	if runtime.GOOS == "linux" {
		err = m.linuxControl(ctx, c, action)
	} else {
		err = m.elevate(ctx, c, action, "")
	}
	if err != nil {
		return err
	}
	if action == "uninstall" {
		for _, name := range []string{"unlock", "config.json", "task.xml"} {
			if err = os.Remove(filepath.Join(c.Root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
func (m *Manager) UpdateCredential(ctx context.Context, password string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := os.Stat(m.c.Root); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var lease *os.File
	for {
		var err error
		lease, err = m.operationLock()
		if err == nil {
			break
		}
		if !errors.Is(err, remote.ErrProfileOwned) {
			return err
		}
		select {
		case <-deadline.Done():
			return errors.New("Startup setup is still in progress; update its unlock credential before rebooting")
		case <-time.After(100 * time.Millisecond):
		}
	}
	defer lease.Close()
	c, installed, err := m.configuration()
	if err != nil || !installed {
		return err
	}
	body, err := protect([]byte(password))
	if err != nil {
		return err
	}
	defer clear(body)
	return privateWrite(filepath.Join(c.Root, "unlock"), body)
}
func ReadCredential(path string) (string, error) {
	body, err := readPrivate(path, 65536)
	if err != nil {
		return "", errors.New("Cannot read the private startup credential; configure startup again from the Server host")
	}
	defer clear(body)
	plain, err := unprotect(body)
	if err != nil {
		return "", errors.New("Cannot unlock the startup credential for this OS account")
	}
	defer clear(plain)
	if len(plain) == 0 || len(plain) > 4096 {
		return "", errors.New("Invalid startup credential")
	}
	return string(plain), nil
}
func (m *Manager) Logs() (string, error) {
	path := filepath.Join(m.c.Root, "server.log")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "No background service log yet.", nil
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("Startup log is not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 32768 {
		_, err = f.Seek(-32768, io.SeekEnd)
		if err != nil {
			return "", err
		}
	}
	body, err := io.ReadAll(io.LimitReader(f, 32768))
	return string(body), err
}
func OpenLog(c Configuration) (*rotatingLog, error) {
	if err := secureDirectory(c.Root); err != nil {
		return nil, err
	}
	path := filepath.Join(c.Root, "server.log")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, errors.New("Startup log is not a regular file")
		}
		if info.Size() > 4<<20 {
			_ = os.Remove(path + ".previous")
			if err = os.Rename(path, path+".previous"); err != nil {
				return nil, err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	return &rotatingLog{file: f, path: path, limit: 4 << 20}, nil
}
func readPrivate(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("Expected a bounded regular startup file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("Startup files must be private to their owner")
	}
	return os.ReadFile(path)
}
func runCommand(parent context.Context, args []string) (string, error) {
	deadline := 25 * time.Second
	if args[0] == "pkexec" {
		deadline = 2 * time.Minute
	}
	args = append([]string(nil), args...)
	if runtime.GOOS == "linux" && (args[0] == "systemctl" || args[0] == "loginctl" || args[0] == "pkexec") {
		args[0] = filepath.Join("/usr/bin", args[0])
		if len(args) > 1 && args[1] == "loginctl" {
			args[1] = "/usr/bin/loginctl"
		}
	}
	if runtime.GOOS == "darwin" && args[0] == "launchctl" {
		args[0] = "/bin/launchctl"
	}
	if runtime.GOOS == "windows" && args[0] == "powershell.exe" {
		args[0] = powershellPath()
	}
	ctx, cancel := context.WithTimeout(parent, deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w: %s", filepath.Base(args[0]), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
