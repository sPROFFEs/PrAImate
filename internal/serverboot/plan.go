// Package serverboot manages an explicitly enabled headless Server for one OS
// account and profile. It does not install anything when the application starts.
package serverboot

import (
	"bytes"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type Configuration struct {
	Database    string            `json:"database"`
	Binary      string            `json:"binary"`
	Home        string            `json:"home"`
	ProfileHome string            `json:"profileHome,omitempty"`
	User        string            `json:"user"`
	UID         string            `json:"uid"`
	Root        string            `json:"root"`
	Tunnel      bool              `json:"tunnel"`
	Environment map[string]string `json:"environment,omitempty"`
}

var profileEnvironment = []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR"}

func (c Configuration) Name() string {
	return fmt.Sprintf("praimate-server-%x", sha256.Sum256([]byte(c.Database)))[:36]
}
func (c Configuration) Args() []string {
	args := []string{"--server", "--managed", "--db", c.Database, "--service-credential", filepath.Join(c.Root, "unlock"), "--startup-config", filepath.Join(c.Root, "config.json")}
	if c.Tunnel {
		args = append(args, "--managed-tunnel")
	}
	return args
}
func absolute(platform, value string) bool {
	if platform == "windows" {
		return len(value) > 2 && value[1] == ':' && (value[2] == '\\' || value[2] == '/') || strings.HasPrefix(value, `\\`)
	}
	return path.IsAbs(value)
}
func (c Configuration) validate(platform string) error {
	for _, value := range []string{c.Database, c.Binary, c.Home, c.Root} {
		if strings.ContainsAny(value, "\r\n\x00") || !absolute(platform, value) {
			return errors.New("Startup paths must be absolute and contain no control characters")
		}
	}
	if c.ProfileHome != "" && (!absolute(platform, c.ProfileHome) || strings.ContainsAny(c.ProfileHome, "\r\n\x00")) {
		return errors.New("Invalid profile home")
	}
	if c.User == "" || c.UID == "" || strings.ContainsAny(c.User+c.UID, "\r\n\x00") {
		return errors.New("A valid OS account is required")
	}
	if platform != "windows" {
		if _, err := strconv.ParseUint(c.UID, 10, 32); err != nil {
			return errors.New("Invalid account ID")
		}
	}
	for name, value := range c.Environment {
		allowed := false
		for _, key := range profileEnvironment {
			if name == key {
				allowed = true
				break
			}
		}
		if !allowed || !absolute(platform, value) || strings.ContainsAny(value, "\r\n\x00") {
			return errors.New("Invalid startup profile environment")
		}
	}
	return nil
}

type plan struct{ Path, Content string }

func xmlText(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func systemdQuote(value string) string {
	return strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(value))
}
func windowsArgument(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	slashes := 0
	for _, c := range value {
		if c == '\\' {
			slashes++
			continue
		}
		if c == '"' {
			b.WriteString(strings.Repeat("\\", slashes*2+1))
		} else {
			b.WriteString(strings.Repeat("\\", slashes))
		}
		slashes = 0
		b.WriteRune(c)
	}
	b.WriteString(strings.Repeat("\\", slashes*2))
	b.WriteByte('"')
	return b.String()
}
func makePlan(platform string, c Configuration, configHome string) (plan, error) {
	if err := c.validate(platform); err != nil {
		return plan{}, err
	}
	name := c.Name()
	args := append([]string{c.Binary}, c.Args()...)
	switch platform {
	case "linux":
		quoted := []string{}
		for _, arg := range args {
			quoted = append(quoted, systemdQuote(arg))
		}
		return plan{Path: filepath.Join(configHome, "systemd", "user", name+".service"), Content: "[Unit]\nDescription=PrAImate Server\nAfter=network-online.target\n\n[Service]\nType=simple\nWorkingDirectory=" + systemdQuote(c.Home) + "\nExecStart=" + strings.Join(quoted, " ") + "\nRestart=on-failure\nRestartSec=5\nKillSignal=SIGINT\nKillMode=mixed\nTimeoutStopSec=30\nUMask=0077\n\n[Install]\nWantedBy=default.target\n"}, nil
	case "darwin":
		list := ""
		for _, arg := range args {
			list += "<string>" + xmlText(arg) + "</string>"
		}
		return plan{Path: "/Library/LaunchDaemons/com.praimate." + name + ".plist", Content: `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>com.praimate.` + name + `</string><key>UserName</key><string>` + xmlText(c.User) + `</string><key>WorkingDirectory</key><string>` + xmlText(c.Home) + `</string><key>EnvironmentVariables</key><dict><key>HOME</key><string>` + xmlText(c.Home) + `</string></dict><key>ProgramArguments</key><array>` + list + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>5</integer></dict></plist>`}, nil
	case "windows":
		quoted := []string{}
		for _, arg := range c.Args() {
			quoted = append(quoted, windowsArgument(arg))
		}
		return plan{Path: filepath.Join(c.Root, "task.xml"), Content: `<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><BootTrigger><Enabled>true</Enabled><Delay>PT15S</Delay></BootTrigger></Triggers><Principals><Principal id="User"><UserId>` + xmlText(c.User) + `</UserId><LogonType>Password</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><Enabled>true</Enabled><Hidden>true</Hidden><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure></Settings><Actions Context="User"><Exec><Command>` + xmlText(c.Binary) + `</Command><Arguments>` + xmlText(strings.Join(quoted, " ")) + `</Arguments><WorkingDirectory>` + xmlText(c.Home) + `</WorkingDirectory></Exec></Actions></Task>`}, nil
	default:
		return plan{}, errors.New("System startup supports Linux with systemd, Windows and macOS")
	}
}
