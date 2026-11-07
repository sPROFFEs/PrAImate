//go:build server

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/serverboot"
	"github.com/sPROFFEs/PrAImate/internal/store"
	"io"
	"net/http"
	"time"
)

func (a *App) startupManager() (*serverboot.Manager, error) {
	if a.dbPath == "" {
		return nil, errors.New("Open this Server profile before configuring system startup")
	}
	return serverboot.New(a.dbPath)
}
func (a *App) ServerStartupStatus() (serverboot.Status, error) {
	m, err := a.startupManager()
	if err != nil {
		return serverboot.Status{}, err
	}
	return m.Status(a.ctx)
}
func (a *App) ServerStartupLogs() (string, error) {
	m, err := a.startupManager()
	if err != nil {
		return "", err
	}
	return m.Logs()
}
func (a *App) InstallServerStartup(databasePassword, windowsPassword string, tunnel bool) error {
	if a.headless || a.detachedClient != nil {
		return errors.New("Authorize system startup from the Server desktop app on its host")
	}
	if err := store.VerifyPassword(a.dbPath, databasePassword); err != nil {
		return errors.New("The current database password could not be verified")
	}
	if a.core == nil {
		if _, sessionErr := serverboot.LoadSession(a.dbPath); sessionErr != nil {
			owner, err := remote.LockProfile(a.dbPath)
			if err != nil {
				return errors.New("Stop the manually managed Server before configuring system startup for this profile")
			}
			owner.Close()
		}
	}
	if tunnel {
		// The live daemon is the DB owner when this window is a local client.
		if a.core == nil {
			if err := a.checkPreparedManagedTunnel(); err != nil {
				return err
			}
		} else if err := a.PrepareServerTunnel(); err != nil {
			return err
		}
	}
	m, err := a.startupManager()
	if err != nil {
		return err
	}
	return m.Install(a.ctx, databasePassword, windowsPassword, tunnel)
}

func (a *App) PrepareServerTunnel() error {
	manager, err := a.tunnelManager()
	if err != nil {
		return err
	}
	return manager.PrepareManaged(a.ctx)
}
func (a *App) checkPreparedManagedTunnel() error {
	session, err := serverboot.LoadSession(a.dbPath)
	if err != nil {
		return err
	}
	client, err := a.managedHTTPClient(session)
	if err != nil {
		return err
	}
	body := []byte(`{"method":"api.CloudflareStatus","params":[]}`)
	req, err := http.NewRequestWithContext(a.ctx, "POST", session.Origin+"/v1/call", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+session.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var payload struct {
		Result struct {
			TokenConfigured bool `json:"tokenConfigured"`
			BinaryInstalled bool `json:"binaryInstalled"`
			Installed       bool `json:"installed"`
			Managed         bool `json:"managed"`
		} `json:"result"`
	}
	if response.StatusCode != 200 || json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&payload) != nil {
		return errors.New("The background Server could not verify its tunnel configuration")
	}
	state := payload.Result
	if !state.TokenConfigured || !state.BinaryInstalled || state.Installed && !state.Managed {
		return errors.New("Prepare the Server-managed tunnel and remove its separate connector service before installing startup")
	}
	return nil
}
func (a *App) ServerStartupControl(action string) (string, error) {
	if a.headless || a.detachedClient != nil {
		return "", errors.New("Manage the OS startup service from the Server desktop app on its host")
	}
	m, err := a.startupManager()
	if err != nil {
		return "", err
	}
	if action == "restart" && a.core != nil {
		return "", errors.New("The foreground GUI owns this profile. Use Start background service to close it and hand over safely")
	}
	var gracefulErr error
	if a.edition.isClient() && (action == "stop" || action == "restart" || action == "uninstall") {
		gracefulErr = a.stopManagedGracefully()
	}
	if err = m.Control(a.ctx, action); err != nil {
		return "", err
	}
	if action == "start" && a.core != nil {
		go func() {
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
			select {
			case <-a.ctx.Done():
				return
			case <-timer.C:
				if a.quit != nil {
					a.quit(a.ctx)
				}
			}
		}()
		return "Starting the service. This foreground window will close to release the profile. Reopen PrAImate to connect to its background workspace.", nil
	}
	if gracefulErr != nil {
		return "Startup operation completed using the OS supervisor. The server did not confirm a graceful shutdown; inspect its logs before resuming interrupted tasks.", nil
	}
	return "Startup service operation completed.", nil
}

func (a *App) stopManagedGracefully() error {
	session, err := serverboot.LoadSession(a.dbPath)
	if err != nil {
		return err
	}
	client, err := a.managedHTTPClient(session)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 25*time.Second)
	defer cancel()
	nonce, err := randomHex(16)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"method": "api.ServerShutdown", "params": []any{}, "requestId": fmt.Sprintf("%d_%s", time.Now().UnixMilli(), nonce)})
	req, err := http.NewRequestWithContext(ctx, "POST", session.Origin+"/v1/call", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+session.Token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, io.LimitReader(response.Body, 65536))
	response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 202 {
		return errors.New("The background Server did not accept graceful shutdown")
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return errors.New("The background Server did not release its profile before the shutdown deadline")
		case <-ticker.C:
			owner, err := remote.LockProfile(a.dbPath)
			if err == nil {
				owner.Close()
				return nil
			}
			if !errors.Is(err, remote.ErrProfileOwned) {
				return err
			}
		}
	}
}
func (e *editionServices) passwordChanged(a *App, password string) error {
	m, err := a.startupManager()
	if err != nil {
		return err
	}
	if err = m.UpdateCredential(a.ctx, password); err != nil {
		return fmt.Errorf("Update the startup credential before the next reboot: %w", err)
	}
	return nil
}

// The native helper exits before Wails, SQLite or Core can be initialized.
func startupAuthorizationCommand(args []string) (bool, int) {
	if len(args) == 0 || args[0] != "--startup-authorize" {
		return false, 0
	}
	if len(args) != 2 {
		return true, 2
	}
	if err := serverboot.RunHelper(args[1]); err != nil {
		return true, 1
	}
	return true, 0
}
