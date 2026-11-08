package serverboot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func managerFixture(t *testing.T) *Manager {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager fixture")
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	m, err := New(filepath.Join(root, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	m.run = func(_ context.Context, args []string) (string, error) {
		if args[0] == "loginctl" {
			return "yes\n", nil
		}
		return "LoadState=loaded\nActiveState=inactive\nUnitFileState=enabled\nResult=success\nExecMainStatus=0\n", nil
	}
	// OS lifecycle tests isolate password authentication, which the binding
	// integration tests exercise against a real encrypted database envelope.
	m.verify = func(string) error { return nil }
	return m
}
func TestInstallDoesNotStartServerAndCredentialIsPrivate(t *testing.T) {
	m := managerFixture(t)
	calls := []string{}
	run := m.run
	m.run = func(ctx context.Context, args []string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return run(ctx, args)
	}
	if err := m.Install(context.Background(), "fixture-database-password", "", true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(calls, "\n"), "--user start") {
		t.Fatal("install interrupted the foreground profile")
	}
	password, err := ReadCredential(filepath.Join(m.c.Root, "unlock"))
	if err != nil || password != "fixture-database-password" {
		t.Fatal("credential did not round trip")
	}
	info, _ := os.Stat(filepath.Join(m.c.Root, "unlock"))
	if info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions are not private")
	}
	status, err := m.Status(context.Background())
	if err != nil || !status.Installed || !status.BootReady || !status.Tunnel || status.Running {
		t.Fatalf("bad status: %+v %v", status, err)
	}
	if strings.Contains(status.Note+status.Error, "fixture-database-password") {
		t.Fatal("credential leaked through status")
	}
}

func TestInvalidLinuxUnitIsNotReportedReadyAtBoot(t *testing.T) {
	for _, state := range []string{"bad-setting", "error"} {
		t.Run(state, func(t *testing.T) {
			m := managerFixture(t)
			if err := m.Install(context.Background(), "fixture-database-password", "", false); err != nil {
				t.Fatal(err)
			}
			run := m.run
			m.run = func(ctx context.Context, args []string) (string, error) {
				if args[0] == "systemctl" {
					return "LoadState=" + state + "\nActiveState=inactive\nUnitFileState=enabled\n", nil
				}
				return run(ctx, args)
			}
			status, err := m.Status(context.Background())
			if err != nil || status.BootReady || status.Running || !status.Enabled || status.Error == "" {
				t.Fatalf("invalid unit appears ready to start: %+v %v", status, err)
			}
		})
	}
}

func TestStartupRecordsCustomManagerRootsAndResolvesNodeSymlinks(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux manager fixture")
	}
	root := t.TempDir()
	stable := filepath.Join(root, "node-versions", "v24.1.0", "installation", "bin")
	shell := filepath.Join(root, "fnm_multishells", "terminal", "bin")
	for _, dir := range []string{stable, shell} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(stable, "node"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(stable, "node"), filepath.Join(shell, "node")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shell+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("NVM_DIR", filepath.Join(root, "custom-nvm"))
	t.Setenv("FNM_DIR", root)
	t.Setenv("FNM_MULTISHELL_PATH", filepath.Dir(shell))
	m := managerFixture(t)
	if err := m.Install(context.Background(), "fixture-database-password", "", false); err != nil {
		t.Fatal(err)
	}
	c, err := ReadConfiguration(filepath.Join(m.c.Root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Environment["NVM_DIR"] != filepath.Join(root, "custom-nvm") || c.Environment["FNM_DIR"] != root || c.Environment["PRAIMATE_NODE_BIN"] != stable {
		t.Fatalf("stable runtime paths were lost: %+v", c.Environment)
	}
	if c.Environment["FNM_MULTISHELL_PATH"] != "" {
		t.Fatal("startup depends on a temporary terminal directory")
	}
}
func TestRemovalRevokesCredentialButKeepsLogsAndOtherServices(t *testing.T) {
	m := managerFixture(t)
	if err := m.Install(context.Background(), "fixture-database-password", "", false); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.c.Root, "server.log"), []byte("previous attempt"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	run := m.run
	m.run = func(ctx context.Context, args []string) (string, error) {
		calls = append(calls, strings.Join(args, " "))
		return run(ctx, args)
	}
	if err := m.Control(context.Background(), "uninstall"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.c.Root, "unlock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("credential retained")
	}
	if strings.Contains(strings.Join(calls, "\n"), "disable-linger") {
		t.Fatal("uninstall affected other services")
	}
	log, err := m.Logs()
	if err != nil || log != "previous attempt" {
		t.Fatal("removal discarded diagnostics")
	}
}
func TestFailedReconfigurationPreservesPreviousCredential(t *testing.T) {
	m := managerFixture(t)
	if err := m.Install(context.Background(), "original-database-password", "", false); err != nil {
		t.Fatal(err)
	}
	m.run = func(context.Context, []string) (string, error) { return "", errors.New("service manager unavailable") }
	if err := m.Install(context.Background(), "replacement-password", "", true); err == nil {
		t.Fatal("failure suppressed")
	}
	password, err := ReadCredential(filepath.Join(m.c.Root, "unlock"))
	if err != nil || password != "original-database-password" {
		t.Fatal("failed update replaced the working credential")
	}
	c, err := ReadConfiguration(filepath.Join(m.c.Root, "config.json"))
	if err != nil || c.Tunnel {
		t.Fatal("failed update replaced the working configuration")
	}
}
func TestPrivateReadsRejectSymlinksAndLogsAreBounded(t *testing.T) {
	m := managerFixture(t)
	if err := secureDirectory(m.c.Root); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other")
	os.WriteFile(other, []byte("do not expose"), 0600)
	if err := os.Symlink(other, filepath.Join(m.c.Root, "unlock")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCredential(filepath.Join(m.c.Root, "unlock")); err == nil {
		t.Fatal("credential symlink accepted")
	}
	os.WriteFile(filepath.Join(m.c.Root, "server.log"), []byte(strings.Repeat("x", 65536)), 0600)
	log, err := m.Logs()
	if err != nil || len(log) != 32768 {
		t.Fatal("log tail is not bounded")
	}
}

func TestStartupCredentialsAreExcludedFromWorkspaceGitBackups(t *testing.T) {
	m := managerFixture(t)
	if err := m.Install(context.Background(), "fixture-database-password", "", false); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(m.c.Database)
	if output, err := exec.Command("git", "init", "--quiet", root).CombinedOutput(); err != nil {
		t.Fatalf("fixture repo: %s %v", output, err)
	}
	for _, file := range []string{"unlock", "config.json"} {
		if output, err := exec.Command("git", "-C", root, "check-ignore", "--quiet", filepath.Join(m.c.Root, file)).CombinedOutput(); err != nil {
			t.Fatalf("startup file is not ignored by workspace backup: %s %v", output, err)
		}
	}
}
func TestConcurrentStartupOperationsCannotReplaceEachOther(t *testing.T) {
	m := managerFixture(t)
	if err := m.Install(context.Background(), "fixture-database-password", "", false); err != nil {
		t.Fatal(err)
	}
	lease, err := m.operationLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	other, err := New(m.c.Database)
	if err != nil {
		t.Fatal(err)
	}
	other.run = func(context.Context, []string) (string, error) {
		t.Error("concurrent operation reached OS manager")
		return "", nil
	}
	if err := other.Control(context.Background(), "disable"); err == nil {
		t.Fatal("concurrent mutation accepted")
	}
}
