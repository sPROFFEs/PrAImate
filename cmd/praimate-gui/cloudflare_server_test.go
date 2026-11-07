//go:build server

package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/store"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloudflareConfigUsesEncryptedStateAndUpdatesLiveGateway(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	t.Setenv("PRAIMATE_REMOTE_LISTEN", "127.0.0.1:0")
	t.Setenv("PRAIMATE_REMOTE_TOKEN", "")
	path := filepath.Join(root, "db.sqlite")
	st, err := store.InitializeWithPassword(path, "synthetic-test-database-password")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := core.New(core.Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &App{core: c, st: st, ctx: ctx, dbPath: path, edition: newEditionServices()}
	a.edition.startError = errors.New("a previous listener attempt failed")
	a.edition.start(a)
	defer a.edition.close()
	if a.edition.startError != nil {
		t.Fatal(a.edition.startError)
	}
	token := "eyJsynthetic_connector_token_only_1234567890"
	config, _ := json.Marshal(map[string]string{"publicURL": "https://Workspace.example.com:443/", "token": token})
	if err = a.SaveCloudflareConfig(string(config)); err != nil {
		t.Fatal(err)
	}
	status, err := a.CloudflareStatus()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(status)
	if !status.TokenConfigured || status.Installed || status.OriginURL == "" || strings.Contains(string(raw), token) {
		t.Fatalf("unexpected public status: %s", raw)
	}
	if !strings.HasPrefix(status.OriginURL, "http://127.0.0.1:") {
		t.Fatalf("Cloudflare needs a complete service URL, got %s", status.OriginURL)
	}
	if _, err = os.Stat(filepath.Join(path+".cloudflare", "token")); !os.IsNotExist(err) {
		t.Fatal("configuration installed a credential/service without explicit installation")
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1/v1/discovery", nil)
	req.Header.Set("Origin", "https://workspace.example.com")
	response := httptest.NewRecorder()
	a.edition.gateway.ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatalf("configured tunnel origin rejected: %d", response.Code)
	}
	pairing, err := a.ServerPairing("Android fixture", "control")
	if err != nil || len(pairing.Code) != 8 {
		t.Fatalf("pairing unavailable: %v", err)
	}
}
