//go:build server

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/serverboot"
)

func nativeClientFixture(t *testing.T, handler http.Handler) (*App, <-chan map[string]any) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "db.sqlite")
	if err := serverboot.SaveSession(path, serverboot.Session{Origin: server.URL, Token: "synthetic-local-owner-credential-for-test"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan map[string]any, 20)
	a := &App{dbPath: path, ctx: ctx, edition: newEditionServices(), emit: func(_ context.Context, _ string, args ...any) {
		select {
		case events <- args[0].(map[string]any):
		case <-ctx.Done():
		}
	}}
	t.Cleanup(func() { cancel(); a.edition.close() })
	return a, events
}
func event(t *testing.T, events <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("native transport did not produce its next event")
		return nil
	}
}

func TestManagedClientConnectsWithoutOpeningTheDatabase(t *testing.T) {
	a, _ := nativeClientFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-local-owner-credential-for-test" {
			t.Error("owner credential was not supplied natively")
		}
		io.WriteString(w, `{"protocol":2}`)
	}))
	if !a.edition.connectManaged(a) || a.core != nil || a.st != nil || a.edition.owner != nil {
		t.Fatal("client initialized a second database owner")
	}
	info, err := a.ManagedServerConnection()
	if err != nil || !info.Active {
		t.Fatalf("connection failed: %+v %v", info, err)
	}
	body, _ := json.Marshal(info)
	if strings.Contains(string(body), "credential") {
		t.Fatal("local owner credential reached the frontend")
	}
}
func TestManagedNativeStreamWaitsForConsumerAcknowledgement(t *testing.T) {
	a, events := nativeClientFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		io.WriteString(w, strings.Repeat("x", 200000))
	}))
	id := strings.Repeat("a", 32)
	if err := a.BeginManagedRequest(id, "/v1/download", "GET", "", "test-client", false); err != nil {
		t.Fatal(err)
	}
	if event(t, events)["status"] != 200 {
		t.Fatal("missing headers")
	}
	first := event(t, events)
	if first["data"] == nil {
		t.Fatal("missing data")
	}
	select {
	case <-events:
		t.Fatal("transport flooded the renderer without acknowledgement")
	case <-time.After(25 * time.Millisecond):
	}
	total := 0
	body, _ := base64.StdEncoding.DecodeString(first["data"].(string))
	total += len(body)
	a.AckManagedRequest(id)
	for {
		next := event(t, events)
		if next["done"] == true {
			break
		}
		if next["error"] != nil {
			t.Fatal(next["error"])
		}
		body, _ = base64.StdEncoding.DecodeString(next["data"].(string))
		total += len(body)
		a.AckManagedRequest(id)
	}
	if total != 200000 {
		t.Fatalf("incomplete response: %d", total)
	}
}
func TestManagedNativeUploadAndCancellationDoNotReplay(t *testing.T) {
	received := make(chan string, 1)
	a, events := nativeClientFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received <- string(data)
		io.WriteString(w, `{"accepted":true}`)
	}))
	id := strings.Repeat("b", 32)
	if err := a.BeginManagedRequest(id, "/v1/upload?name=test", "PUT", "text/plain", "test-client", true); err != nil {
		t.Fatal(err)
	}
	if err := a.WriteManagedRequest(id, base64.StdEncoding.EncodeToString([]byte("hola ñ 🐒")), true); err != nil {
		t.Fatal(err)
	}
	if event(t, events)["status"] != 200 {
		t.Fatal("upload failed")
	}
	next := event(t, events)
	if next["data"] == nil {
		t.Fatal("no receipt")
	}
	a.AckManagedRequest(id)
	event(t, events)
	if <-received != "hola ñ 🐒" {
		t.Fatal("upload bytes changed")
	}
	a.CancelManagedRequest(id)
	if err := a.WriteManagedRequest(id, "YQ==", false); err == nil {
		t.Fatal("completed upload accepted more input")
	}
}
func TestManagedNativeTransportRejectsOffHostPaths(t *testing.T) {
	a, _ := nativeClientFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("unsafe request reached the server") }))
	for _, path := range []string{"https://other.example/v1/status", "//other.example/v1/status", "/elsewhere", "/v1/status\r\nAuthorization: injected"} {
		if err := a.BeginManagedRequest(strings.Repeat("c", 32), path, "GET", "", "test-client", false); err == nil {
			t.Fatalf("accepted %q", path)
		}
	}
}
