package serverboot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows"
	"os/exec"
	"os/user"
	"strings"
	"time"
)

func elevate(ctx context.Context, c Configuration, action, password string) error {
	return elevatedRequest(ctx, c, action, password, func(binary, request string) error {
		verb, _ := windows.UTF16PtrFromString("runas")
		file, err := windows.UTF16PtrFromString(binary)
		if err != nil {
			return err
		}
		args, err := windows.UTF16PtrFromString("--startup-authorize " + windowsArgument(request))
		if err != nil {
			return err
		}
		return windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE)
	})
}
func authorizedControl(req helperRequest) error {
	c := req.Configuration
	u, err := user.Current()
	if err != nil || u.Uid != c.UID || !strings.EqualFold(u.Username, c.User) {
		return errors.New("Authorize startup using the same Windows account that owns this profile")
	}
	p, err := makePlan("windows", c, "")
	if err != nil {
		return err
	}
	input := map[string]string{"name": c.Name(), "user": c.User, "password": req.Password, "xml": p.Content, "sid": c.UID}
	script := `$ErrorActionPreference='Stop';$r=[Console]::In.ReadToEnd()|ConvertFrom-Json;`
	switch req.Action {
	case "install":
		script += `$s=New-Object -ComObject 'Schedule.Service';$s.Connect();$f=$s.GetFolder('\');$acl='D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;'+$r.sid+')';try{$null=$f.RegisterTask($r.name,$r.xml,6,$r.user,$r.password,1,$acl)}catch{throw 'Task registration failed. Verify your Windows account password and batch logon rights.'}`
	case "start":
		script += `Start-ScheduledTask -TaskName $r.name`
	case "stop":
		script += `$t=Get-ScheduledTask -TaskName $r.name;if($t.State -eq 'Running'){$t|Stop-ScheduledTask}`
	case "restart":
		script += `$t=Get-ScheduledTask -TaskName $r.name;if($t.State -eq 'Running'){$t|Stop-ScheduledTask};Start-ScheduledTask -TaskName $r.name`
	case "enable":
		script += `$null=Enable-ScheduledTask -TaskName $r.name`
	case "disable":
		script += `$null=Disable-ScheduledTask -TaskName $r.name`
	case "uninstall":
		script += `$t=Get-ScheduledTask -TaskName $r.name -ErrorAction SilentlyContinue;if($t){$t|Stop-ScheduledTask -ErrorAction SilentlyContinue;$t|Unregister-ScheduledTask -Confirm:$false}`
	default:
		return errors.New("Unknown startup task operation")
	}
	body, _ := json.Marshal(input)
	defer clear(body)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, powershellPath(), "-NoProfile", "-NonInteractive", "-Command", script)
	hideWindow(cmd)
	cmd.Stdin = bytes.NewReader(body)
	// Credential input stays on an anonymous pipe. Error output is deliberately
	// bounded to an action result; neither the JSON nor the password is logged.
	if err = cmd.Run(); err != nil {
		return errors.New("Windows startup operation failed; verify account credentials, task permissions and Event Viewer")
	}
	return nil
}
