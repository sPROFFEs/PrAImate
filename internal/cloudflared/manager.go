// Package cloudflared manages a dedicated connector for one PrAImate profile.
// It never takes over the system-wide cloudflared service.
package cloudflared

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Configuration struct {
	PublicURL string `json:"publicURL"`
	Token     string `json:"token,omitempty"`
}

// Status deliberately has no credential field. Tokens are write-only to clients.
type Status struct {
	Managed         bool   `json:"managed"`
	PublicURL       string `json:"publicURL"`
	OriginURL       string `json:"originURL"`
	TokenConfigured bool   `json:"tokenConfigured"`
	BinaryInstalled bool   `json:"binaryInstalled"`
	Installed       bool   `json:"installed"`
	Running         bool   `json:"running"`
	Ready           bool   `json:"ready"`
	ServiceName     string `json:"serviceName"`
	Platform        string `json:"platform"`
	Note            string `json:"note"`
	Error           string `json:"error,omitempty"`
}
type Manager struct {
	mu            sync.Mutex
	root          string
	load          func(context.Context) ([]byte, error)
	save          func(context.Context, []byte) error
	run           func(context.Context, []string) (string, error)
	managedCancel context.CancelFunc
	managedPort   int
}

func New(root string, load func(context.Context) ([]byte, error), save func(context.Context, []byte) error) *Manager {
	return &Manager{root: root, load: load, save: save, run: runCommand}
}
func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\t\\") {
		return "", errors.New("Public URL must be an HTTPS origin, without credentials, path, query or fragment")
	}
	u.Path = ""
	u.Host = strings.ToLower(u.Host)
	if u.Port() == "443" {
		u.Host = u.Hostname()
		if strings.Contains(u.Host, ":") {
			u.Host = "[" + u.Host + "]"
		}
	}
	return u.String(), nil
}
func validateToken(token string) error {
	if len(token) < 32 || len(token) > 16384 {
		return errors.New("Paste only the tunnel connector token from Cloudflare, not its installation command")
	}
	for _, c := range token {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_-=./+", c)) {
			return errors.New("Invalid connector token; paste the token only")
		}
	}
	return nil
}
func (m *Manager) configuration(ctx context.Context) (Configuration, error) {
	var c Configuration
	raw, err := m.load(ctx)
	if err == nil && len(raw) > 0 {
		err = json.Unmarshal(raw, &c)
	}
	return c, err
}
func (m *Manager) PublicURL(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, err := m.configuration(ctx)
	return c.PublicURL, err
}
func (m *Manager) Save(ctx context.Context, c Configuration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	c.PublicURL, err = NormalizeURL(c.PublicURL)
	if err != nil {
		return err
	}
	old, err := m.configuration(ctx)
	if err != nil {
		return err
	}
	c.Token = strings.TrimSpace(c.Token)
	if c.Token == "" {
		c.Token = old.Token
	}
	if err = validateToken(c.Token); err != nil {
		return err
	}
	// A running connector reads its token once. Explicitly stop before rotating it.
	if c.Token != old.Token {
		if m.managedCancel != nil {
			return errors.New("Stop the background Server before replacing its managed connector token")
		}
		if _, err = os.Stat(filepath.Join(m.root, "service.json")); err == nil {
			return errors.New("Remove the connector service before replacing its token; existing sessions remain on the server")
		}
	}
	raw, _ := json.Marshal(c)
	return m.save(ctx, raw)
}
func (m *Manager) identity() string {
	return fmt.Sprintf("praimate-cloudflare-%x", sha256.Sum256([]byte(m.root)))[:34]
}
func (m *Manager) binary() string {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return filepath.Join(m.root, "cloudflared"+suffix)
}

type installation struct {
	Port int `json:"port"`
}

func (m *Manager) installed() (installation, bool) {
	var i installation
	b, err := os.ReadFile(filepath.Join(m.root, "service.json"))
	if err != nil {
		return i, false
	}
	err = json.Unmarshal(b, &i)
	return i, err == nil && i.Port > 0 && i.Port <= 65535
}
func (m *Manager) Status(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status(ctx)
}
func (m *Manager) status(ctx context.Context) (Status, error) {
	c, err := m.configuration(ctx)
	if err != nil {
		return Status{}, err
	}
	s := Status{PublicURL: c.PublicURL, TokenConfigured: c.Token != "", ServiceName: m.identity(), Platform: runtime.GOOS}
	if info, err := os.Stat(m.binary()); err == nil {
		s.BinaryInstalled = info.Mode().IsRegular()
	}
	i, ok := m.installed()
	if m.managedCancel != nil {
		s.Managed = true
		s.Running = true
		s.Note = "This connector is supervised by the background PrAImate Server and starts with it before login."
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/ready", m.managedPort), nil)
		if response, err := client.Do(req); err == nil {
			s.Ready = response.StatusCode == 200
			response.Body.Close()
		}
		client.CloseIdleConnections()
		return s, nil
	}
	s.Installed = ok
	s.Note = "The connector is a per-user service. PrAImate Server must also be running. Configure the public hostname and origin in Cloudflare."
	if runtime.GOOS == "linux" {
		s.Note += " To stay online after logout, the OS account needs systemd lingering (configured by your administrator)."
	}
	if runtime.GOOS == "windows" {
		s.Note += " Windows runs the connector as a hidden task at user logon."
	}
	if ok {
		p, err := servicePlan(runtime.GOOS, m.root, m.identity(), i.Port)
		if err != nil {
			return s, err
		}
		out, err := m.run(ctx, p.Status)
		s.Running = err == nil && p.isRunning(out)
		if err != nil {
			s.Error = "Service status unavailable. Check that your user service manager is running."
		}
		if s.Running {
			client := &http.Client{Timeout: time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			req, _ := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://127.0.0.1:%d/ready", i.Port), nil)
			response, err := client.Do(req)
			if err == nil {
				s.Ready = response.StatusCode == 200
				_ = response.Body.Close()
			}
			client.CloseIdleConnections()
		}
	}
	return s, nil
}

// Control acts only on this profile's dedicated service. Commands never contain
// the connector token. The service manager reads a private credential file.
func (m *Manager) Control(ctx context.Context, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.managedCancel != nil {
		return errors.New("This connector is managed by the background Server; change its System startup configuration instead")
	}
	switch action {
	case "install", "start", "stop", "restart", "uninstall":
	default:
		return errors.New("Unknown tunnel service operation")
	}
	c, err := m.configuration(ctx)
	if err != nil {
		return err
	}
	i, installed := m.installed()
	if action == "install" {
		if installed {
			return errors.New("Connector service already installed")
		}
		if _, err = NormalizeURL(c.PublicURL); err != nil {
			return err
		}
		if err = validateToken(c.Token); err != nil {
			return err
		}
		if err = privateDirectory(m.root); err != nil {
			return err
		}
		if _, err = os.Stat(m.binary()); errors.Is(err, os.ErrNotExist) {
			if err = downloadBinary(ctx, m.binary()); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		i.Port = listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		p, err := servicePlan(runtime.GOOS, m.root, m.identity(), i.Port)
		if err != nil {
			return err
		}
		if _, err = os.Lstat(p.Path); err == nil {
			return errors.New("A service definition already exists at the managed path; inspect it before installing")
		}
		if err = privateWrite(filepath.Join(m.root, "token"), []byte(c.Token)); err != nil {
			return err
		}
		if err = privateWrite(p.Path, []byte(p.Content)); err != nil {
			return err
		}
		// Record ownership before registration so a failed installation can be removed.
		raw, _ := json.Marshal(i)
		if err = privateWrite(filepath.Join(m.root, "service.json"), raw); err != nil {
			return err
		}
		for _, command := range p.Install {
			if _, err = m.run(ctx, command); err != nil {
				return fmt.Errorf("Register connector service: %w", err)
			}
		}
		return nil
	}
	if !installed {
		return errors.New("Install the connector service first")
	}
	p, err := servicePlan(runtime.GOOS, m.root, m.identity(), i.Port)
	if err != nil {
		return err
	}
	commands := map[string][][]string{"start": p.Start, "stop": p.Stop, "restart": p.Restart, "uninstall": p.Remove}[action]
	if runtime.GOOS == "darwin" {
		_, statusErr := m.run(ctx, p.Status)
		if statusErr != nil && (action == "stop" || action == "uninstall") {
			commands = nil
		}
		if statusErr != nil && action == "restart" {
			commands = p.Start
		}
		if statusErr == nil && action == "start" {
			commands = p.Restart
		}
	}
	for _, command := range commands {
		if _, err = m.run(ctx, command); err != nil {
			return fmt.Errorf("Connector %s: %w", action, err)
		}
	}
	if action == "uninstall" {
		for _, path := range []string{p.Path, filepath.Join(m.root, "token"), filepath.Join(m.root, "service.json")} {
			if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if runtime.GOOS == "linux" {
			if _, err = m.run(ctx, []string{"systemctl", "--user", "daemon-reload"}); err != nil {
				return err
			}
		}
	}
	return nil
}
