package cloudflared

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigurationRejectsURLsAndCommands(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com/path", "https://example.com?token=x", "https://example.com#x"} {
		if _, err := NormalizeURL(raw); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if got, err := NormalizeURL("https://Example.com/"); err != nil || got != "https://example.com" {
		t.Fatalf("%s %v", got, err)
	}
	if got, err := NormalizeURL("https://Example.com:443/"); err != nil || got != "https://example.com" {
		t.Fatalf("default HTTPS port not normalized: %s %v", got, err)
	}
	if err := validateToken("cloudflared service install secret"); err == nil {
		t.Fatal("accepted a command as token")
	}
}

func TestServiceLifecycleIsOwnedAndHealthIsReportedSeparately(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service lifecycle fixture")
	}
	ctx := context.Background()
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	root := filepath.Join(base, "connector")
	var stored []byte
	m := New(root, func(context.Context) ([]byte, error) { return stored, nil }, func(_ context.Context, b []byte) error { stored = append([]byte(nil), b...); return nil })
	token := "eyJsynthetic_connector_token_only_1234567890"
	if err := m.Save(ctx, Configuration{PublicURL: "https://workspace.example.com", Token: token}); err != nil {
		t.Fatal(err)
	}
	if err := privateDirectory(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.binary(), []byte("fixture, never execute"), 0700); err != nil {
		t.Fatal(err)
	}
	active := false
	var commands []string
	m.run = func(_ context.Context, args []string) (string, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		if strings.Contains(command, token) {
			t.Fatal("token in process arguments")
		}
		if len(args) > 2 && args[2] == "start" {
			active = true
		}
		if strings.Contains(command, "disable --now") || len(args) > 2 && args[2] == "stop" {
			active = false
		}
		if strings.Contains(command, "ActiveState") {
			if active {
				return "active\n", nil
			}
			return "inactive\n", nil
		}
		return "", nil
	}
	if err := m.Control(ctx, "install"); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil || !status.Installed || status.Running || status.Ready {
		t.Fatalf("installation started the tunnel: %+v %v", status, err)
	}
	info, err := os.Stat(filepath.Join(root, "token"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("credential is not private: %v", err)
	}
	if err = m.Control(ctx, "start"); err != nil {
		t.Fatal(err)
	}
	status, err = m.Status(ctx)
	if err != nil || !status.Running || status.Ready {
		t.Fatalf("process must not imply connectivity: %+v %v", status, err)
	}
	i, _ := m.installed()
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", i.Port))
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ready" {
			t.Error("wrong health path")
		}
		w.WriteHeader(200)
	}), ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	defer server.Close()
	status, err = m.Status(ctx)
	if err != nil || !status.Ready {
		t.Fatalf("healthy connector not detected: %+v %v", status, err)
	}
	if err = m.Save(ctx, Configuration{PublicURL: status.PublicURL, Token: token + "new"}); err == nil {
		t.Fatal("rotated an installed connector token without removal")
	}
	if err = m.Control(ctx, "uninstall"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "token")); !os.IsNotExist(err) {
		t.Fatal("service credential was retained after removal")
	}
	status, err = m.Status(ctx)
	if err != nil || status.Installed || !status.TokenConfigured {
		t.Fatalf("removal changed saved config: %+v %v", status, err)
	}
	if len(commands) == 0 {
		t.Fatal("no service lifecycle commands")
	}
}

func TestTokenIsWriteOnlyAndServiceUsesPrivateFile(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var stored []byte
	m := New(root, func(context.Context) ([]byte, error) { return stored, nil }, func(_ context.Context, b []byte) error { stored = append([]byte(nil), b...); return nil })
	token := "eyJsynthetic_connector_token_only_1234567890"
	if err := m.Save(ctx, Configuration{PublicURL: "https://workspace.example.com", Token: token}); err != nil {
		t.Fatal(err)
	}
	status, err := m.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), token) || !status.TokenConfigured {
		t.Fatalf("unsafe status: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(root, "token")); !os.IsNotExist(err) {
		t.Fatal("saving config must not materialize a service secret")
	}
	for _, platform := range []string{"linux", "darwin", "windows"} {
		p, err := servicePlan(platform, root, "praimate-test", 24567)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(p.Content, token) || !strings.Contains(p.Content, "--token-file") {
			t.Fatal("connector token must never appear in service arguments")
		}
	}
	if err := m.Save(ctx, Configuration{PublicURL: "https://next.example.com"}); err != nil {
		t.Fatal(err)
	}
	var cfg Configuration
	_ = json.Unmarshal(stored, &cfg)
	if cfg.Token != token {
		t.Fatal("empty edit discarded the stored token")
	}
}
