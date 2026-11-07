//go:build server

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
)

const serverSetting = "remote.server.config.v1"

type ServerConfiguration struct {
	AutoStart      bool     `json:"autoStart"`
	Listen         string   `json:"listen"`
	TLSCert        string   `json:"tlsCert"`
	TLSKey         string   `json:"tlsKey"`
	AllowedOrigins []string `json:"allowedOrigins"`
}

type ServerStatusInfo struct {
	Configuration       ServerConfiguration  `json:"configuration"`
	ActiveConfiguration *ServerConfiguration `json:"activeConfiguration,omitempty"`
	Running             bool                 `json:"running"`
	Pending             bool                 `json:"pending"`
	OriginURL           string               `json:"originURL"`
	PublicURL           string               `json:"publicURL"`
	Error               string               `json:"error,omitempty"`
}

func serverConfiguration(config remote.Config) ServerConfiguration {
	return ServerConfiguration{Listen: config.Listen, TLSCert: config.TLSCert, TLSKey: config.TLSKey, AllowedOrigins: append([]string{}, config.AllowedOrigins...)}
}

func (a *App) loadServerConfiguration() (ServerConfiguration, bool, error) {
	s := ServerConfiguration{Listen: "127.0.0.1:8844", AllowedOrigins: []string{}}
	c, err := a.requireCore()
	if err != nil {
		return s, false, err
	}
	raw, err := c.GetSetting(a.ctx, core.ScopeCLI, serverSetting)
	if err != nil {
		return s, false, err
	}
	if len(raw) == 0 {
		return s, false, nil
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, true, errors.New("Saved Server configuration is invalid; save it again from Server settings")
	}
	if s.AllowedOrigins == nil {
		s.AllowedOrigins = []string{}
	}
	return s, true, nil
}

func (e *editionServices) prepareServerSettingsLocked(a *App) (bool, error) {
	s, saved, err := a.loadServerConfiguration()
	if err != nil {
		return false, err
	}
	e.settingsLoaded = true
	// Explicit headless flags and startup environment keep their precedence.
	if e.config.Listen == "" {
		if saved && !s.AutoStart {
			e.startupDisabled = true
			return false, nil
		}
		if saved {
			e.config.Listen = s.Listen
		}
	}
	if e.config.TLSCert == "" {
		e.config.TLSCert = s.TLSCert
	}
	if e.config.TLSKey == "" {
		e.config.TLSKey = s.TLSKey
	}
	if len(e.config.AllowedOrigins) == 0 {
		e.config.AllowedOrigins = s.AllowedOrigins
	}
	return true, nil
}

func normalizeServerConfiguration(s *ServerConfiguration) error {
	s.Listen = strings.TrimSpace(s.Listen)
	s.TLSCert, s.TLSKey = strings.TrimSpace(s.TLSCert), strings.TrimSpace(s.TLSKey)
	if s.Listen == "" {
		return errors.New("A listen address and port are required, for example 127.0.0.1:8844")
	}
	if len(s.AllowedOrigins) > 32 {
		return errors.New("At most 32 browser origins may be allowed")
	}
	origins := []string{}
	seen := map[string]bool{}
	for _, origin := range s.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("Invalid browser origin %q: use only scheme, hostname and optional port", origin)
		}
		if !seen[origin] {
			origins = append(origins, origin)
			seen[origin] = true
		}
	}
	s.AllowedOrigins = origins
	_, err := remote.ValidateListener(remote.Config{Listen: s.Listen, TLSCert: s.TLSCert, TLSKey: s.TLSKey})
	return err
}

func (a *App) ServerStatus() (ServerStatusInfo, error) {
	s, _, err := a.loadServerConfiguration()
	if err != nil {
		return ServerStatusInfo{}, err
	}
	e := &a.edition
	e.mu.Lock()
	defer e.mu.Unlock()
	status := ServerStatusInfo{Configuration: s, Running: e.gateway != nil, ActiveConfiguration: e.activeConfiguration}
	if e.startError != nil {
		status.Error = e.startError.Error()
	}
	if status.Running {
		status.OriginURL = serverOriginURL(e.address, e.config.TLSCert != "")
		desired := s
		desired.AutoStart = false
		status.Pending = !reflect.DeepEqual(&desired, e.activeConfiguration)
	}
	status.PublicURL, err = e.tunnelLocked(a).PublicURL(a.ctx)
	return status, err
}

func serverOriginURL(address string, secure bool) string {
	if address == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		host = "::1"
		if ip.To4() != nil {
			host = "127.0.0.1"
		}
	}
	scheme := "http"
	if secure {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// Saving is deliberately independent of applying: remote users can persist a
// new address without losing their current connection halfway through a write.
func (a *App) SaveServerConfig(raw string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	var s ServerConfiguration
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return errors.New("Invalid Server configuration")
	}
	if err := normalizeServerConfiguration(&s); err != nil {
		return err
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return c.SetSetting(a.ctx, core.ScopeCLI, serverSetting, encoded)
}

// Controls only the transport. Core tasks, native terminals and the GUI retain
// their existing application context throughout a listener restart.
func (a *App) ServerListener(action string) error {
	if _, err := a.requireCore(); err != nil {
		return err
	}
	if action != "start" && action != "stop" && action != "restart" {
		return errors.New("Unknown listener action")
	}
	e := &a.edition
	e.listenerMu.Lock()
	defer e.listenerMu.Unlock()
	if action == "stop" {
		e.closeListener()
		e.mu.Lock()
		e.startError = nil
		e.mu.Unlock()
		return nil
	}
	s, _, err := a.loadServerConfiguration()
	if err != nil {
		return err
	}
	if err = normalizeServerConfiguration(&s); err != nil {
		return err
	}
	e.mu.Lock()
	previous, wasRunning := e.config, e.gateway != nil
	e.mu.Unlock()
	if wasRunning && action == "start" {
		return errors.New("Server is already running; apply settings to restart it")
	}
	e.closeListener()
	e.mu.Lock()
	e.config = remote.Config{Listen: s.Listen, TLSCert: s.TLSCert, TLSKey: s.TLSKey, AllowedOrigins: s.AllowedOrigins, Token: previous.Token, Administrator: previous.Administrator}
	e.settingsLoaded = true
	e.startupDisabled = false
	e.mu.Unlock()
	e.startListener(a)
	e.mu.Lock()
	err = e.startError
	e.mu.Unlock()
	if err != nil && wasRunning {
		// An occupied new port must not strand an otherwise working server.
		e.mu.Lock()
		e.config = previous
		e.mu.Unlock()
		e.startListener(a)
		e.mu.Lock()
		rollbackErr := e.startError
		if rollbackErr == nil {
			e.startError = fmt.Errorf("New settings could not be applied; previous listener restored: %w", err)
		} else {
			e.startError = fmt.Errorf("Apply failed: %v; restoring previous listener failed: %w", err, rollbackErr)
		}
		err = e.startError
		e.mu.Unlock()
	}
	return err
}

func (a *App) remoteServerListener(action string) (string, error) {
	if action != "start" && action != "stop" && action != "restart" {
		return "", errors.New("Unknown listener action")
	}
	// Let the durable RPC receipt commit before closing the gateway that owns
	// it. Never replay this action automatically after a lost connection.
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-a.ctx.Done():
			return
		case <-timer.C:
		}
		if err := a.ServerListener(action); err != nil {
			a.edition.mu.Lock()
			a.edition.startError = err
			a.edition.mu.Unlock()
		}
	}()
	return "Listener change scheduled. Reconnect to the configured address after it completes.", nil
}

func (a *App) serverDevices() (*remote.Devices, error) {
	if a.edition.gateway != nil {
		return a.edition.gateway.Devices(), nil
	}
	return remote.NewDevices(remoteCoreState{a.core})
}

func (a *App) ServerDevices() ([]remote.Device, error) {
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	a.edition.mu.Lock()
	defer a.edition.mu.Unlock()
	d, err := a.serverDevices()
	if err != nil {
		return nil, err
	}
	return d.List(), nil
}

func (a *App) RevokeServerDevice(id string) error {
	if _, err := a.requireCore(); err != nil {
		return err
	}
	a.edition.mu.Lock()
	defer a.edition.mu.Unlock()
	d, err := a.serverDevices()
	if err != nil {
		return err
	}
	return d.Revoke(id)
}
