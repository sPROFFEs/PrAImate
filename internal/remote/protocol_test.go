package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestViewerCannotExecuteMutationsOrAdminMethods(t *testing.T) {
	registry := NewRegistry()
	calls := 0
	registry.Register("api.Create", "admin", true, func() bool { calls++; return true })
	g, err := New(Config{Token: testToken, State: &memoryState{}, Registry: registry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	pairing, err := g.CreatePairing("viewer", "viewer")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := g.devices.Pair(pairing.Code, "viewer")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"method":"api.Create","params":[],"requestId":"0000000000000_request123"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 403 || calls != 0 {
		t.Fatalf("viewer executed admin method: %d / %d", w.Code, calls)
	}
	devices := g.devices.List()
	if err := g.devices.Revoke(devices[0].ID); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	g.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("revoked device retained API access")
	}
}
func TestReplayRecoversOnlyMissingEventsAndSignalsNewServerEpoch(t *testing.T) {
	g, err := New(Config{Token: testToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	g.Publish("term:data:one", map[string]string{"data": "first"})
	cursor := fmt.Sprintf("%s:1", g.epoch)
	g.Publish("term:data:one", map[string]string{"data": "second"})
	_, unsub, replay, resync, err := g.subscribeFrom(cursor)
	if err != nil {
		t.Fatal(err)
	}
	defer unsub()
	if resync || len(replay) != 1 || !strings.Contains(string(replay[0]), "second") {
		t.Fatal("event replay lost or duplicated output")
	}
	_, unsub2, _, resync, err := g.subscribeFrom("old-server:1")
	if err != nil {
		t.Fatal(err)
	}
	defer unsub2()
	if !resync {
		t.Fatal("server restart did not require snapshots")
	}
}
func TestInterruptedOperationsAreNotReexecutedAndExpiredIDsFailClosed(t *testing.T) {
	state := &memoryState{}
	record := Operation{ID: fmt.Sprintf("%d_request123", time.Now().UnixMilli()), Owner: "owner", Status: "running"}
	raw, _ := json.Marshal([]Operation{record})
	state.Save(context.Background(), "operations", raw)
	calls := 0
	o, err := NewOperations(context.Background(), state, func(context.Context, string, json.RawMessage) (any, error) { calls++; return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	p := Principal{ID: "owner", Role: "admin"}
	op, err := o.Get(p, record.ID)
	if err != nil || op.Status != "interrupted" || calls != 0 {
		t.Fatal("restart replayed an interrupted operation")
	}
	id := fmt.Sprintf("%d_request456", time.Now().Add(-time.Hour).UnixMilli())
	if _, err = o.Submit(p, id, "create", nil); err == nil {
		t.Fatal("expired unknown operation could be replayed")
	}
}
func TestDownloadStreamsRangesInsideWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "example.bin"), []byte("abcdef"), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{Token: testToken, Files: &Files{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	req := httptest.NewRequest("GET", "/v1/download?workspace="+root+"&path=example.bin", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, req)
	body, _ := io.ReadAll(w.Result().Body)
	if w.Code != http.StatusPartialContent || string(body) != "cde" {
		t.Fatalf("range download: %d %q", w.Code, body)
	}
}
func TestProfileHasOnlyOneServerOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.sqlite")
	one, err := LockProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if two, err := LockProfile(path); err == nil {
		two.Close()
		t.Fatal("a second server acquired the same profile")
	}
}

func TestTerminalControlRequiresAnIndependentClientIdentity(t *testing.T) {
	registry := NewRegistry()
	registry.Register("api.WriteTerminal", "control", false, func(ctx context.Context) string { return Identity(ctx).ClientID })
	g, err := New(Config{Token: testToken, Registry: registry}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, fixture := range []struct {
		client string
		status int
	}{{"", 400}, {"unsafe/client", 400}, {"browser_123", 200}} {
		req := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"method":"api.WriteTerminal","params":[]}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		req.Header.Set("X-Praimate-Client", fixture.client)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)
		if w.Code != fixture.status {
			t.Fatalf("client %q: status %d, want %d", fixture.client, w.Code, fixture.status)
		}
	}
}
