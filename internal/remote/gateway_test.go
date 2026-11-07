package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testToken = "test-only-token-with-at-least-32-characters"

func TestGatewayAuthenticatesBeforeCallingBackend(t *testing.T) {
	calls := 0
	g, err := New(Config{Token: testToken}, func(context.Context, string, json.RawMessage) (any, error) { calls++; return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"", "wrong", testToken} {
		r := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"method":"terminals.list","params":{}}`))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		want := http.StatusUnauthorized
		if token == testToken {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Fatalf("status %d, want %d: %s", w.Code, want, w.Body)
		}
	}
	if calls != 1 {
		t.Fatalf("unauthenticated backend call: %d", calls)
	}
}

func TestGatewayRejectsUntrustedOriginsAndMalformedInput(t *testing.T) {
	g, err := New(Config{Token: testToken}, func(context.Context, string, json.RawMessage) (any, error) {
		t.Fatal("invalid request reached backend")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"method":"x","extra":true}`, `{"method":"x"} {}`, strings.Repeat("x", (1<<20)+1)} {
		r := httptest.NewRequest("POST", "/v1/call", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid input: %d", w.Code)
		}
	}
	r := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"method":"x"}`))
	r.Header.Set("Authorization", "Bearer "+testToken)
	r.Header.Set("Origin", "https://untrusted.example")
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("origin accepted: %d", w.Code)
	}
}

func TestGatewayRequiresTLSOutsideLoopback(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:0", "[::]:0", "example.com:0"} {
		g, err := New(Config{Token: testToken, Listen: listen}, func(context.Context, string, json.RawMessage) (any, error) { return nil, nil })
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Start(context.Background()); err == nil {
			t.Fatalf("cleartext external listener allowed: %s", listen)
		}
	}
	if _, err := New(Config{Token: "short"}, nil); err == nil {
		t.Fatal("weak token accepted")
	}
}

func TestSlowEventConsumerIsDisconnected(t *testing.T) {
	g, err := New(Config{Token: testToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, unsubscribe, err := g.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	for i := 0; i < 200; i++ {
		g.Publish("term:data:example", map[string]string{"data": "YQ=="})
	}
	for range ch {
	}
}

func TestGatewayLiveEventsAndSnapshotRecoverySignal(t *testing.T) {
	g, err := New(Config{Token: testToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(g)
	t.Cleanup(server.Close)
	t.Cleanup(func() { _ = g.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/events", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	initial, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(initial, `"snapshotsRequired":true`) {
		t.Fatalf("missing reconnect signal: %q, %v", initial, err)
	}
	_, _ = reader.ReadString('\n')
	g.Publish("term:data:shared", map[string]any{"data": "YQ==", "startOffset": 0, "endOffset": 1})
	line, err := reader.ReadString('\n')
	if err != nil || !strings.Contains(line, `"name":"term:data:shared"`) || !strings.Contains(line, `"endOffset":1`) {
		t.Fatalf("live terminal event lost: %q, %v", line, err)
	}
}

func TestGatewayLoopbackListenerLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, err := New(Config{Token: testToken, Listen: "127.0.0.1:0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr, err := g.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	req, _ := http.NewRequest("GET", "http://"+addr+"/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", resp.StatusCode)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Start(ctx); err == nil {
		t.Fatal("closed gateway restarted")
	}
}
