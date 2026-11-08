package serverboot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func (m *Manager) linuxControl(ctx context.Context, c Configuration, action string) error {
	name := c.Name() + ".service"
	if action == "install" {
		if _, err := m.run(ctx, []string{"systemctl", "--user", "daemon-reload"}); err != nil {
			return err
		}
		action = "enable"
	}
	if action == "enable" {
		out, err := m.run(ctx, []string{"loginctl", "show-user", c.UID, "--property=Linger", "--value"})
		if err != nil || strings.TrimSpace(out) != "yes" {
			if _, err = m.run(ctx, []string{"loginctl", "enable-linger", c.UID}); err != nil {
				if _, err = m.run(ctx, []string{"pkexec", "loginctl", "enable-linger", c.UID}); err != nil {
					return fmt.Errorf("Allow this account to start before login using loginctl enable-linger %s: %w", c.User, err)
				}
			}
		}
	}
	if action == "uninstall" {
		if _, err := m.run(ctx, []string{"systemctl", "--user", "disable", "--now", name}); err != nil {
			return err
		}
		home, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		p, err := makePlan("linux", c, home)
		if err != nil {
			return err
		}
		if err = os.Remove(p.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		_, err = m.run(ctx, []string{"systemctl", "--user", "daemon-reload"})
		return err
	}
	_, err := m.run(ctx, []string{"systemctl", "--user", action, name})
	return err
}
func (m *Manager) nativeStatus(ctx context.Context, c Configuration) (enabled, running, ready bool, note string, err error) {
	switch runtime.GOOS {
	case "linux":
		out, e := m.run(ctx, []string{"systemctl", "--user", "show", c.Name() + ".service", "--property=LoadState,ActiveState,UnitFileState,Result,ExecMainStatus", "--no-pager"})
		if e != nil {
			return false, false, false, "", e
		}
		values := map[string]string{}
		for _, line := range strings.Split(out, "\n") {
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[key] = value
			}
		}
		enabled = values["UnitFileState"] == "enabled"
		if values["LoadState"] == "not-found" {
			return enabled, false, false, "", errors.New("Startup configuration is present but its systemd unit is missing; install it again")
		}
		if values["LoadState"] != "loaded" {
			return enabled, false, false, "", fmt.Errorf("Startup unit could not be loaded (state %s). Update startup configuration and inspect the user service logs", values["LoadState"])
		}
		running = values["ActiveState"] == "active" || values["ActiveState"] == "activating"
		linger, e := m.run(ctx, []string{"loginctl", "show-user", c.UID, "--property=Linger", "--value"})
		ready = enabled && e == nil && strings.TrimSpace(linger) == "yes"
		note = "Runs as your OS account, including before login. Disabling startup leaves systemd lingering unchanged for other user services."
		if enabled && !ready {
			note = "Enable account lingering to start before login; use Enable system startup to authorize it."
		}
		if values["ActiveState"] == "failed" {
			err = fmt.Errorf("Background server failed (result %s, exit %s). Inspect service logs", values["Result"], values["ExecMainStatus"])
		}
		return
	case "darwin":
		p, _ := makePlan("darwin", c, "")
		if _, err = os.Stat(p.Path); err != nil {
			return false, false, false, "", errors.New("Startup daemon is missing; install it from the Server host again")
		}
		out, e := m.run(ctx, []string{"launchctl", "print-disabled", "system"})
		if e != nil {
			return false, false, false, "", e
		}
		label := "com.praimate." + c.Name()
		enabled = !strings.Contains(out, `"`+label+`" => true`)
		out, e = m.run(ctx, []string{"launchctl", "print", "system/" + label})
		running = e == nil && strings.Contains(out, "state = running")
		ready = enabled
		note = "A system LaunchDaemon starts this profile under your OS account before login. Lifecycle changes require host administrator authorization."
		return enabled, running, ready, note, nil
	case "windows":
		code := `$t=Get-ScheduledTask -TaskName '` + c.Name() + `' -ErrorAction Stop;$i=Get-ScheduledTaskInfo -TaskName '` + c.Name() + `';@{enabled=[bool]$t.Settings.Enabled;running=($t.State -eq 'Running');result=[long]$i.LastTaskResult}|ConvertTo-Json -Compress`
		out, e := m.run(ctx, []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", code})
		if e != nil {
			return false, false, false, "", e
		}
		var state struct {
			Enabled bool  `json:"enabled"`
			Running bool  `json:"running"`
			Result  int64 `json:"result"`
		}
		if err = json.Unmarshal([]byte(strings.TrimSpace(out)), &state); err != nil {
			return
		}
		note = "Windows Task Scheduler starts this profile before login with network-capable account credentials. Update startup configuration after changing your Windows account password."
		if !state.Running && state.Result != 0 && state.Result != 267009 && state.Result != 267011 {
			err = fmt.Errorf("Last background task result: 0x%X. Inspect service logs and account credentials", state.Result)
		}
		return state.Enabled, state.Running, state.Enabled, note, err
	default:
		return false, false, false, "", errors.New("Unsupported startup service platform")
	}
}

// Helper is an explicitly elevated entrypoint used only by macOS/Windows.
// Requests carry paths and account identifiers; Windows passwords are encrypted
// with DPAPI and never placed on a command line or in a task definition.
type helperRequest struct {
	Configuration Configuration `json:"configuration"`
	Action        string        `json:"action"`
	Password      string        `json:"password,omitempty"`
}

func RunHelper(requestPath string) (result error) {
	if filepath.Base(requestPath) != "request" || !strings.HasPrefix(filepath.Base(filepath.Dir(requestPath)), ".startup-request-") {
		return errors.New("Invalid startup authorization request")
	}
	defer func() {
		receipt := struct {
			Error string `json:"error,omitempty"`
		}{}
		if result != nil {
			receipt.Error = result.Error()
		}
		body, _ := json.Marshal(receipt)
		if err := atomicWrite(filepath.Join(filepath.Dir(requestPath), "result"), body, 0644); err != nil {
			result = errors.Join(result, err)
		}
	}()
	body, err := readPrivate(requestPath, 65536)
	if err != nil {
		return err
	}
	plain, err := unprotect(body)
	clear(body)
	if err != nil {
		return err
	}
	defer clear(plain)
	var req helperRequest
	if err = json.Unmarshal(plain, &req); err != nil {
		return errors.New("Invalid startup authorization request")
	}
	if err = req.Configuration.validate(runtime.GOOS); err != nil {
		return err
	}
	if req.Configuration.Root != req.Configuration.Database+".startup" || filepath.Dir(filepath.Dir(requestPath)) != req.Configuration.Root {
		return errors.New("Startup authorization does not match its profile")
	}
	if err = validateRequester(filepath.Dir(requestPath), req.Configuration.UID); err != nil {
		return err
	}
	if !map[string]bool{"install": true, "enable": true, "disable": true, "start": true, "stop": true, "restart": true, "uninstall": true}[req.Action] {
		return errors.New("Invalid startup operation")
	}
	return authorizedControl(req)
}
