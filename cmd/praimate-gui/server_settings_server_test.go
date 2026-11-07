//go:build server

package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/remote"
	"github.com/sPROFFEs/PrAImate/internal/store"
)

func serverSettingsFixture(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	for _, key := range []string{"PRAIMATE_REMOTE_LISTEN", "PRAIMATE_REMOTE_TOKEN", "PRAIMATE_REMOTE_TLS_CERT", "PRAIMATE_REMOTE_TLS_KEY", "PRAIMATE_REMOTE_ORIGINS"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(root, "db.sqlite")
	st, err := store.InitializeWithPassword(path, "synthetic-server-settings-password")
	if err != nil {
		t.Fatal(err)
	}
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a := &App{core: c, st: st, ctx: ctx, dbPath: path, edition: newEditionServices()}
	t.Cleanup(func() { a.edition.close(); cancel(); st.Close() })
	return a
}

func TestServerListenerRestoresPreviousAddressAfterBindFailure(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	before, _ := a.ServerStatus()
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	raw, _ := json.Marshal(ServerConfiguration{Listen: occupied.Addr().String()})
	if err := a.SaveServerConfig(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("restart"); err == nil {
		t.Fatal("occupied port accepted")
	}
	status, _ := a.ServerStatus()
	if !status.Running || !status.Pending || !strings.Contains(status.Error, "previous listener restored") {
		t.Fatalf("no recovery from bind failure: %+v", status)
	}
	// The previous configured ephemeral port is allowed to choose a new port.
	if status.ActiveConfiguration.Listen != before.ActiveConfiguration.Listen {
		t.Fatal("previous settings not restored")
	}
}

func TestServerDevicesPersistAcrossRestartAndCanBeRevokedWhileStopped(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	pair, err := a.ServerPairing("Direct phone", "control")
	if err != nil {
		t.Fatal(err)
	}
	status, _ := a.ServerStatus()
	raw, _ := json.Marshal(map[string]string{"code": pair.Code})
	response, err := http.Post(status.OriginURL+"/v1/pair", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("pairing failed %d: %s", response.StatusCode, body)
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	devices, err := a.ServerDevices()
	if err != nil || len(devices) != 1 {
		t.Fatalf("missing device: %v %v", devices, err)
	}
	if err := a.ServerListener("restart"); err != nil {
		t.Fatal(err)
	}
	status, _ = a.ServerStatus()
	req, _ := http.NewRequest("GET", status.OriginURL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("paired device rejected after restart")
	}
	if err := a.ServerListener("stop"); err != nil {
		t.Fatal(err)
	}
	if err := a.RevokeServerDevice(devices[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	status, _ = a.ServerStatus()
	req, _ = http.NewRequest("GET", status.OriginURL+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+result.Token)
	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("revoked device accepted after restart")
	}
}

func TestServerStartupPreferenceDoesNotGetOverriddenByCloudflare(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.SaveServerConfig(`{"autoStart":false,"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	a.edition.start(a)
	if err := a.SaveCloudflareConfig(`{"publicURL":"https://workspace.example.com","token":"eyJsynthetic_connector_token_only_1234567890"}`); err != nil {
		t.Fatal(err)
	}
	a.edition.start(a)
	status, _ := a.ServerStatus()
	if status.Running {
		t.Fatal("saving a connector ignored the disabled startup preference")
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("stop"); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveCloudflareConfig(`{"publicURL":"https://workspace.example.com"}`); err != nil {
		t.Fatal(err)
	}
	status, _ = a.ServerStatus()
	if status.Running {
		t.Fatal("saving connector settings undid Stop access")
	}
}

func TestServerSettingsPersistAndControlListenerWithoutStoppingCore(t *testing.T) {
	a := serverSettingsFixture(t)
	a.edition.start(a)
	status, err := a.ServerStatus()
	if err != nil || status.Running || status.Configuration.Listen != "127.0.0.1:8844" {
		t.Fatalf("initial status: %+v %v", status, err)
	}
	config := ServerConfiguration{AutoStart: true, Listen: "127.0.0.1:0", AllowedOrigins: []string{"https://mobile.example.com"}}
	raw, _ := json.Marshal(config)
	if err := a.SaveServerConfig(string(raw)); err != nil {
		t.Fatal(err)
	}
	if a.edition.gateway != nil {
		t.Fatal("saving settings opened a listener")
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	status, err = a.ServerStatus()
	if err != nil || !status.Running || !strings.HasPrefix(status.OriginURL, "http://127.0.0.1:") || status.Pending {
		t.Fatalf("running status: %+v %v", status, err)
	}
	response, err := http.Get(status.OriginURL + "/v1/discovery")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("discovery: %d", response.StatusCode)
	}
	if _, err := a.ServerPairing("Direct mobile", "control"); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("stop"); err != nil {
		t.Fatal(err)
	}
	status, _ = a.ServerStatus()
	if status.Running || status.OriginURL != "" {
		t.Fatalf("stopped listener advertised: %+v", status)
	}
	if a.ctx.Err() != nil {
		t.Fatal("stopping remote access cancelled the application")
	}
	if _, err := a.core.GetSetting(a.ctx, core.ScopeCLI, serverSetting); err != nil {
		t.Fatalf("Core unavailable: %v", err)
	}
	// A subsequent application start honors the encrypted startup preference.
	a.edition.settingsLoaded = false
	a.edition.config.Listen = ""
	a.edition.start(a)
	status, _ = a.ServerStatus()
	if !status.Running || !status.Configuration.AutoStart {
		t.Fatalf("saved configuration not restored: %+v", status)
	}
}

func TestServerSettingsRejectInvalidConfigAndRetainWorkingListener(t *testing.T) {
	a := serverSettingsFixture(t)
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	before, _ := a.ServerStatus()
	for _, raw := range []string{
		`{"listen":"0.0.0.0:8844"}`,
		`{"listen":"127.0.0.1:99999"}`,
		`{"listen":"127.0.0.1:8844","allowedOrigins":["https://example.com/path"]}`,
		`{"listen":"127.0.0.1:8844","tlsCert":"missing.pem","tlsKey":"missing.key"}`,
	} {
		if err := a.SaveServerConfig(raw); err == nil {
			t.Fatalf("invalid configuration accepted: %s", raw)
		}
	}
	after, _ := a.ServerStatus()
	if !after.Running || after.OriginURL != before.OriginURL || after.Configuration.Listen != before.Configuration.Listen {
		t.Fatal("invalid settings disrupted working server")
	}
}

func TestServerSettingsStartANetworkListenerWithTLS(t *testing.T) {
	a := serverSettingsFixture(t)
	// Generate file fixtures from httptest's synthetic certificate, never from
	// a user's certificate store or private files.
	source := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer source.Close()
	certificate := source.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	certPath, keyPath := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(ServerConfiguration{Listen: "0.0.0.0:0", TLSCert: certPath, TLSKey: keyPath})
	if err := a.SaveServerConfig(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	status, _ := a.ServerStatus()
	origin, err := url.Parse(status.OriginURL)
	if err != nil || origin.Scheme != "https" || !net.ParseIP(origin.Hostname()).IsLoopback() {
		t.Fatalf("incorrect TLS origin: %s", status.OriginURL)
	}
	response, err := source.Client().Get(status.OriginURL + "/v1/discovery")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("TLS discovery: %d", response.StatusCode)
	}
}

func TestServerListenerRestartPreservesBootstrapAuthentication(t *testing.T) {
	a := serverSettingsFixture(t)
	a.edition.config.Token = "synthetic-administrator-token-32-characters"
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "restart"} {
		if err := a.ServerListener(action); err != nil {
			t.Fatal(err)
		}
		status, _ := a.ServerStatus()
		req, _ := http.NewRequest("GET", status.OriginURL+"/v1/devices", nil)
		req.Header.Set("Authorization", "Bearer synthetic-administrator-token-32-characters")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("administrator rejected after %s: %d", action, resp.StatusCode)
		}
	}
	if a.edition.config.Token != "" {
		t.Fatal("plaintext administrator credential retained")
	}
}

func TestServerRemoteListenerRestartRetainsTheCompletedOperation(t *testing.T) {
	a := serverSettingsFixture(t)
	const token = "synthetic-listener-restart-administrator-token"
	a.edition.config.Token = token
	if err := a.SaveServerConfig(`{"listen":"127.0.0.1:0"}`); err != nil {
		t.Fatal(err)
	}
	if err := a.ServerListener("start"); err != nil {
		t.Fatal(err)
	}
	status, _ := a.ServerStatus()
	old := a.edition.gateway
	id := fmt.Sprintf("%d_listenerRestartFixture", time.Now().UnixMilli())
	body, _ := json.Marshal(map[string]any{"method": "api.ServerListener", "params": []string{"restart"}, "requestId": id})
	client := &http.Client{Timeout: time.Second}
	request, _ := http.NewRequest("POST", status.OriginURL+"/v1/call", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("restart operation not accepted: %d", response.StatusCode)
	}
	readOperation := func(origin string) remote.Operation {
		t.Helper()
		req, _ := http.NewRequest("GET", origin+"/v1/operations/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("operation unavailable: %d", resp.StatusCode)
		}
		var operation remote.Operation
		if err := json.NewDecoder(resp.Body).Decode(&operation); err != nil {
			t.Fatal(err)
		}
		return operation
	}
	deadline := time.Now().Add(time.Second)
	for {
		op := readOperation(status.OriginURL)
		if op.Status == "completed" {
			break
		}
		if op.Status != "running" || time.Now().After(deadline) {
			t.Fatalf("restart receipt not completed: %+v", op)
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case <-old.Done():
	case <-time.After(4 * time.Second):
		t.Fatal("scheduled restart did not stop the old gateway")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		status, _ = a.ServerStatus()
		if status.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("gateway did not restart")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if operation := readOperation(status.OriginURL); operation.Status != "completed" {
		t.Fatalf("receipt lost across restart: %+v", operation)
	}
	if a.ctx.Err() != nil {
		t.Fatal("remote listener restart cancelled Core")
	}
}
