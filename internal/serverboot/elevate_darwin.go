package serverboot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func elevate(ctx context.Context, c Configuration, action, password string) error {
	return elevatedRequest(ctx, c, action, password, func(binary, request string) error {
		quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
		command := quote(binary) + " --startup-authorize " + quote(request)
		script := "do shell script " + strconv.Quote(command) + " with administrator privileges"
		cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
		if err := cmd.Run(); err != nil {
			if _, receiptErr := os.Stat(filepath.Join(filepath.Dir(request), "result")); receiptErr == nil {
				return nil
			}
			return errors.New("Administrator authorization was cancelled or the startup operation failed")
		}
		return nil
	})
}
func authorizedControl(req helperRequest) error {
	c := req.Configuration
	if os.Geteuid() != 0 {
		return errors.New("Authorize this startup operation as an administrator")
	}
	u, err := user.LookupId(c.UID)
	if err != nil || u.Username != c.User || c.UID == "0" {
		return errors.New("The daemon must use the profile's non-root OS account")
	}
	info, err := os.Stat(c.Database)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(stat.Uid), 10) != c.UID {
		return errors.New("The startup account does not own this database")
	}
	p, err := makePlan("darwin", c, "")
	if err != nil {
		return err
	}
	label := "system/com.praimate." + c.Name()
	run := func(args ...string) error { _, err := runCommand(context.Background(), args); return err }
	switch req.Action {
	case "install":
		var old []byte
		if info, statErr := os.Lstat(p.Path); statErr == nil {
			if !info.Mode().IsRegular() || info.Size() > 65536 {
				return errors.New("Refusing to replace an unsafe daemon definition")
			}
			old, err = os.ReadFile(p.Path)
			if err != nil {
				return err
			}
		}
		if err = atomicWrite(p.Path, []byte(p.Content), 0644); err != nil {
			return err
		}
		if err = os.Chown(p.Path, 0, 0); err != nil {
			return err
		}
		if err = run("launchctl", "enable", label); err != nil {
			if old != nil {
				_ = atomicWrite(p.Path, old, 0644)
			} else {
				_ = os.Remove(p.Path)
			}
			return err
		}
		return nil
	case "enable", "disable":
		return run("launchctl", req.Action, label)
	case "start":
		if err := run("launchctl", "print", label); err == nil {
			return run("launchctl", "kickstart", label)
		}
		return run("launchctl", "bootstrap", "system", p.Path)
	case "restart":
		_ = run("launchctl", "bootout", label)
		return run("launchctl", "bootstrap", "system", p.Path)
	case "stop":
		if err := run("launchctl", "print", label); err != nil {
			return nil
		}
		return run("launchctl", "bootout", label)
	case "uninstall":
		_ = run("launchctl", "bootout", label)
		if err = os.Remove(p.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return errors.New("Unknown daemon operation")
}
