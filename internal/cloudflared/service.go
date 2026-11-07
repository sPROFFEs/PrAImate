package cloudflared

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type plan struct {
	Path, Content                         string
	Install, Start, Stop, Restart, Remove [][]string
	Status                                []string
	isRunning                             func(string) bool
}

func xmlText(value string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}
func systemdQuote(value string) string {
	return strconv.Quote(strings.NewReplacer("%", "%%", "$", "$$").Replace(value))
}
func servicePlan(platform, root, name string, port int) (plan, error) {
	token := filepath.Join(root, "token")
	binary := filepath.Join(root, "cloudflared")
	arguments := []string{"tunnel", "--no-autoupdate", "--metrics", fmt.Sprintf("127.0.0.1:%d", port), "run", "--token-file", token}
	p := plan{}
	switch platform {
	case "linux":
		base, err := os.UserConfigDir()
		if err != nil {
			return p, err
		}
		p.Path = filepath.Join(base, "systemd", "user", name+".service")
		args := []string{systemdQuote(binary)}
		for _, arg := range arguments {
			args = append(args, systemdQuote(arg))
		}
		p.Content = "[Unit]\nDescription=PrAImate Cloudflare connector\nAfter=network-online.target\n\n[Service]\nType=simple\nExecStart=" + strings.Join(args, " ") + "\nRestart=on-failure\nRestartSec=5\nUMask=0077\nNoNewPrivileges=true\nStandardOutput=journal\nStandardError=journal\n\n[Install]\nWantedBy=default.target\n"
		p.Install = [][]string{{"systemctl", "--user", "daemon-reload"}, {"systemctl", "--user", "enable", name}}
		p.Start = [][]string{{"systemctl", "--user", "start", name}}
		p.Stop = [][]string{{"systemctl", "--user", "stop", name}}
		p.Restart = [][]string{{"systemctl", "--user", "restart", name}}
		p.Remove = [][]string{{"systemctl", "--user", "disable", "--now", name}}
		p.Status = []string{"systemctl", "--user", "show", name, "--property=ActiveState", "--value"}
		p.isRunning = func(out string) bool { return strings.TrimSpace(out) == "active" }
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return p, err
		}
		u, err := user.Current()
		if err != nil {
			return p, err
		}
		label := "com.praimate." + name
		domain := "gui/" + u.Uid
		p.Path = filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		args := "<string>" + xmlText(binary) + "</string>"
		for _, arg := range arguments {
			args += "<string>" + xmlText(arg) + "</string>"
		}
		p.Content = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array>` + args + `</array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>5</integer><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>`
		// Writing LaunchAgents installs autostart; bootstrap happens on Start.
		p.Start = [][]string{{"launchctl", "bootstrap", domain, p.Path}}
		p.Stop = [][]string{{"launchctl", "bootout", domain, p.Path}}
		p.Restart = [][]string{{"launchctl", "kickstart", "-k", domain + "/" + label}}
		p.Remove = [][]string{{"launchctl", "bootout", domain, p.Path}}
		p.Status = []string{"launchctl", "print", domain + "/" + label}
		p.isRunning = func(out string) bool { return strings.Contains(out, "state = running") }
	case "windows":
		u, err := user.Current()
		if err != nil {
			return p, err
		}
		binary += ".exe"
		p.Path = filepath.Join(root, "task.xml")
		// Task Scheduler's Hidden flag hides the task, not a console window.
		// A hidden PowerShell host keeps the console connector invisible too.
		quotePS := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
		args := []string{quotePS(binary)}
		for _, arg := range arguments {
			args = append(args, quotePS(arg))
		}
		script := "& " + strings.Join(args, " ") + "; exit $LASTEXITCODE"
		systemRoot := os.Getenv("SystemRoot")
		if systemRoot == "" {
			systemRoot = `C:\Windows`
		}
		powershell := filepath.Join(systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		p.Content = `<?xml version="1.0" encoding="UTF-8"?><Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task"><Triggers><LogonTrigger><Enabled>true</Enabled><UserId>` + xmlText(u.Username) + `</UserId></LogonTrigger></Triggers><Principals><Principal id="User"><UserId>` + xmlText(u.Username) + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>LeastPrivilege</RunLevel></Principal></Principals><Settings><Hidden>true</Hidden><DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries><StopIfGoingOnBatteries>false</StopIfGoingOnBatteries><ExecutionTimeLimit>PT0S</ExecutionTimeLimit><MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy><RestartOnFailure><Interval>PT1M</Interval><Count>999</Count></RestartOnFailure></Settings><Actions Context="User"><Exec><Command>` + xmlText(powershell) + `</Command><Arguments>` + xmlText("-NoProfile -NonInteractive -WindowStyle Hidden -Command "+windowsArgument(script)) + `</Arguments></Exec></Actions></Task>`
		p.Install = [][]string{{"schtasks.exe", "/Create", "/TN", name, "/XML", p.Path}}
		p.Start = [][]string{{"schtasks.exe", "/Run", "/TN", name}}
		p.Stop = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Stop-ScheduledTask -TaskName '" + name + "' -ErrorAction Stop"}}
		p.Restart = append(append([][]string{}, p.Stop...), p.Start...)
		p.Remove = [][]string{{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$task=Get-ScheduledTask -TaskName '" + name + "' -ErrorAction SilentlyContinue; if($task){$task | Stop-ScheduledTask -ErrorAction Stop; $task | Unregister-ScheduledTask -Confirm:$false -ErrorAction Stop}"}}
		p.Status = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "(Get-ScheduledTask -TaskName '" + name + "' -ErrorAction Stop).State.ToString()"}
		p.isRunning = func(out string) bool { return strings.TrimSpace(out) == "Running" }
	default:
		return p, errors.New("Cloudflare connector services support Linux, Windows and macOS")
	}
	return p, nil
}
func windowsArgument(value string) string {
	// CommandLineToArgvW quoting, including backslashes before quotes and at end.
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
func runCommand(parent context.Context, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	hideWindow(cmd)
	// Only the service manager is launched. No application/model secrets are passed.
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s failed: %w (check the per-user service manager)", filepath.Base(args[0]), err)
	}
	return string(out), nil
}
