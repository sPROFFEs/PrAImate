package serverboot

import (
	"encoding/xml"
	"strings"
	"testing"
)

func example() Configuration {
	return Configuration{Database: "/home/test/profile/db.sqlite", Binary: "/opt/PrAImate/praimate-gui", Home: "/home/test", User: "test", UID: "1000", Root: "/home/test/profile/db.sqlite.startup", Tunnel: true}
}

func TestLinuxStartsAtBootWithSavedProfileAndNoSecretInUnit(t *testing.T) {
	c := example()
	p, err := makePlan("linux", c, "/home/test/.config")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--server", "--managed", "--db", "--service-credential", c.Database, "Restart=on-failure", "WantedBy=default.target", "--managed-tunnel"} {
		if !strings.Contains(p.Content, want) {
			t.Fatalf("missing %q in unit", want)
		}
	}
	if strings.Contains(p.Content, "PASSWORD=") || strings.Contains(p.Content, "--listen") {
		t.Fatal("unit overrides saved settings or contains a password")
	}
}

func TestMacUsesSystemDaemonUnderTheProfileAccount(t *testing.T) {
	c := example()
	p, err := makePlan("darwin", c, "/unused")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.Path, "/Library/LaunchDaemons/") || !strings.Contains(p.Content, "<key>UserName</key><string>test</string>") || strings.Contains(p.Content, "LaunchAgents") {
		t.Fatal("daemon depends on user login or runs under another account")
	}
	if err := xml.Unmarshal([]byte(p.Content), new(any)); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsBootTaskUsesPasswordLogonAndEscapesArguments(t *testing.T) {
	c := example()
	c.Binary = `C:\Program Files\PrAImate\praimate-gui.exe`
	c.Home = `C:\Users\Test`
	c.User = `HOST\Test`
	c.Database = `C:\Users\Test\db.sqlite`
	c.Root = c.Database + ".startup"
	p, err := makePlan("windows", c, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BootTrigger", "<LogonType>Password</LogonType>", "<RunLevel>LeastPrivilege</RunLevel>", "<ExecutionTimeLimit>PT0S</ExecutionTimeLimit>"} {
		if !strings.Contains(p.Content, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(p.Content, "S4U") || strings.Contains(p.Content, "InteractiveToken") {
		t.Fatal("task needs a GUI session or loses network credentials")
	}
	if err := xml.Unmarshal([]byte(p.Content), new(any)); err != nil {
		t.Fatal(err)
	}
}

func TestPlansRejectInjectedAndRelativeConfiguration(t *testing.T) {
	for _, change := range []func(*Configuration){func(c *Configuration) { c.Database = "relative.db" }, func(c *Configuration) { c.Binary = "/bin/gui\nExecStart=/bin/sh" }, func(c *Configuration) { c.User = "test\nroot" }} {
		c := example()
		change(&c)
		if _, err := makePlan("linux", c, "/tmp"); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
}
