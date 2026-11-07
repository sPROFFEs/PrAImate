//go:build server

package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/sPROFFEs/PrAImate/internal/cloudflared"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
)

const tunnelSetting = "remote.cloudflare.connector.v1"

// Called with edition.mu held. The setting lives inside the encrypted database,
// outside the generic GUI preference API.
func (e *editionServices) tunnelLocked(a *App) *cloudflared.Manager {
	if e.tunnel == nil {
		root, _ := filepath.Abs(a.dbPath + ".cloudflare")
		e.tunnel = cloudflared.New(root, func(ctx context.Context) ([]byte, error) { return a.core.GetSetting(ctx, core.ScopeCLI, tunnelSetting) }, func(ctx context.Context, raw []byte) error {
			return a.core.SetSetting(ctx, core.ScopeCLI, tunnelSetting, raw)
		})
	}
	return e.tunnel
}
func (a *App) tunnelManager() (*cloudflared.Manager, error) {
	if _, err := a.requireCore(); err != nil {
		return nil, err
	}
	a.edition.mu.Lock()
	defer a.edition.mu.Unlock()
	return a.edition.tunnelLocked(a), nil
}
func (a *App) CloudflareStatus() (cloudflared.Status, error) {
	m, err := a.tunnelManager()
	if err != nil {
		return cloudflared.Status{}, err
	}
	status, err := m.Status(a.ctx)
	a.edition.mu.Lock()
	if a.edition.gateway != nil {
		status.OriginURL = serverOriginURL(a.edition.address, a.edition.config.TLSCert != "")
	}
	a.edition.mu.Unlock()
	return status, err
}
func (a *App) SaveCloudflareConfig(raw string) error {
	var config cloudflared.Configuration
	if err := json.Unmarshal([]byte(raw), &config); err != nil {
		return errors.New("Invalid Cloudflare configuration")
	}
	m, err := a.tunnelManager()
	if err != nil {
		return err
	}
	if err = m.Save(a.ctx, config); err != nil {
		return err
	}
	publicURL, err := m.PublicURL(a.ctx)
	if err != nil {
		return err
	}
	a.edition.mu.Lock()
	g := a.edition.gateway
	a.edition.mu.Unlock()
	if g == nil {
		_, saved, err := a.loadServerConfiguration()
		if err != nil {
			return err
		}
		// A saved listener configuration is controlled explicitly from Server.
		// Connector configuration must not undo a user's Stop access action.
		if saved {
			return nil
		}
		a.edition.start(a)
		a.edition.mu.Lock()
		g = a.edition.gateway
		startErr := a.edition.startError
		a.edition.mu.Unlock()
		if startErr != nil {
			return startErr
		}
	}
	if g == nil {
		return errors.New("Start the main PrAImate Server before configuring its connector")
	}
	return g.SetPublicOrigin(publicURL)
}
func (a *App) CloudflareService(action string) error {
	m, err := a.tunnelManager()
	if err != nil {
		return err
	}
	if action == "install" || action == "start" || action == "restart" {
		a.edition.mu.Lock()
		running := a.edition.gateway != nil
		a.edition.mu.Unlock()
		if !running {
			return errors.New("Start PrAImate Server before starting its connector")
		}
	}
	return m.Control(a.ctx, action)
}
func (a *App) ServerPairing(name, role string) (remote.Pairing, error) {
	if _, err := a.requireCore(); err != nil {
		return remote.Pairing{}, err
	}
	a.edition.mu.Lock()
	g := a.edition.gateway
	a.edition.mu.Unlock()
	if g == nil {
		return remote.Pairing{}, errors.New("Remote server is not running")
	}
	return g.CreatePairing(name, role)
}
