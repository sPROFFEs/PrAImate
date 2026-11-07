//go:build server

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/serverboot"
)

func TestServerStartupRejectsWrongPasswordBeforeWritingOrInstalling(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.InstallServerStartup("incorrect fixture password", "", false); err == nil {
		t.Fatal("incorrect database password accepted")
	}
	if _, err := os.Stat(a.dbPath + ".startup"); !os.IsNotExist(err) {
		t.Fatal("failed authorization left a startup credential or configuration")
	}
}
func TestRemoteClientsCannotRegisterOrControlHostStartup(t *testing.T) {
	a := NewApp()
	catalog := a.remoteMethods()
	for _, name := range []string{"api.InstallServerStartup", "api.ServerStartupControl", "api.BeginManagedRequest", "api.ManagedServerConnection", "api.UseForegroundServer"} {
		if _, exists := catalog.Lookup(name); exists {
			t.Fatalf("host-only method exposed remotely: %s", name)
		}
	}
	for _, name := range []string{"api.ServerStartupStatus", "api.ServerStartupLogs", "api.PrepareServerTunnel"} {
		method, ok := catalog.Lookup(name)
		if !ok || method.Scope != "admin" {
			t.Fatalf("missing administrator startup method: %s", name)
		}
		if _, err := catalog.Call(remote.WithPrincipal(context.Background(), remote.Principal{ID: "viewer", Role: "viewer"}), name, []byte(`[]`)); err == nil {
			t.Fatal("viewer reached startup administration")
		}
	}
}
func TestHeadlessManagedStartupUsesSavedListenerWithoutAGraphicalSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows credentials are tested through the DPAPI backend")
	}
	a := serverSettingsFixture(t)
	t.Setenv("PRAIMATE_SERVER_SUPERVISED", "")
	t.Setenv("PRAIMATE_DB_PASSWORD", "")
	t.Setenv("PRAIMATE_DB_PASSWORD_FILE", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := launcher.SaveConfig(&launcher.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0","autoStart":false}`); err != nil {
		t.Fatal(err)
	}
	if err := a.st.Close(); err != nil {
		t.Fatal(err)
	}
	root := a.dbPath + ".startup"
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "unlock")
	if err := os.WriteFile(credential, []byte("synthetic-server-settings-password"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &readyOutput{ready: make(chan struct{})}
	stderr := &readyOutput{}
	done := make(chan int, 1)
	go func() {
		done <- runHeadlessServer(ctx, []string{"--db", a.dbPath, "--managed", "--service-credential", credential}, strings.NewReader(""), output, stderr)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("managed server exited %d: %s", code, stderr.String())
			}
		case <-time.After(20 * time.Second):
			t.Error("managed server did not stop")
		}
	})
	select {
	case <-output.ready:
	case <-time.After(15 * time.Second):
		t.Fatal("managed server did not become ready:", stderr.String())
	}
	session, err := serverboot.LoadSession(a.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	client := &App{ctx: context.Background(), dbPath: a.dbPath, edition: newEditionServices()}
	if !client.edition.connectManaged(client) {
		t.Fatal("GUI did not discover the headless owner")
	}
	info, err := client.ManagedServerConnection()
	if err != nil || !info.Active {
		t.Fatalf("native GUI did not connect: %v", err)
	}
	request, _ := http.NewRequest("POST", session.Origin+"/v1/call", strings.NewReader(`{"method":"api.ServerStatus","params":[]}`))
	request.Header.Set("Authorization", "Bearer "+session.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload struct {
		Result ServerStatusInfo `json:"result"`
	}
	if err = json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Result.ActiveConfiguration == nil || payload.Result.ActiveConfiguration.Listen != "127.0.0.1:0" {
		t.Fatal("managed startup ignored the saved listener configuration")
	}
	client.shutdown(context.Background())
	request, _ = http.NewRequest("GET", session.Origin+"/v1/status", nil)
	request.Header.Set("Authorization", "Bearer "+session.Token)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal("closing GUI stopped the server:", err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("server stopped after closing client GUI")
	}
	if err := client.stopManagedGracefully(); err != nil {
		t.Fatal("native GUI could not stop the background Core gracefully:", err)
	}
	if strings.Contains(output.String()+stderr.String(), "synthetic-server-settings-password") {
		t.Fatal("database password reached service output")
	}
}
