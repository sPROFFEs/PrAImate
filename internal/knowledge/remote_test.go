package knowledge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteKnowledgeRoundTripAndConfinement(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Authentication\nJWT tokens authenticate requests.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(HTTPHandler(dir, "test-token"))
	defer server.Close()
	client := RemoteClient{Endpoint: server.URL, APIKey: "test-token"}
	ctx := context.Background()
	for _, action := range []string{"health", "query", "search", "read"} {
		text, err := client.Call(ctx, action, map[string]any{"question": "JWT", "query": "tokens", "path": "guide.md", "budget": 200})
		if err != nil || text == "" {
			t.Fatalf("%s: %q %v", action, text, err)
		}
	}
	if _, err := (RemoteClient{Endpoint: server.URL}).Call(ctx, "health", nil); err == nil {
		t.Fatal("authentication bypass")
	}
	for _, path := range []string{"../outside", "/etc/passwd", ".praimate-index/index.json"} {
		if _, err := client.Call(ctx, "read", map[string]any{"path": path}); err == nil {
			t.Fatalf("unconfined read: %s", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	_ = os.WriteFile(outside, []byte("private"), 0600)
	if err := os.Symlink(outside, filepath.Join(dir, "escape.md")); err == nil {
		if _, err := client.Call(ctx, "read", map[string]any{"path": "escape.md"}); err == nil {
			t.Fatal("symlink escaped root")
		}
	}
}

func TestRemoteKnowledgeRedirectAndOutputBudget(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("redirect followed with credentials") }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	if _, err := (RemoteClient{Endpoint: redirect.URL, APIKey: "private"}).Call(context.Background(), "health", nil); err == nil {
		t.Fatal("redirect accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(RemoteResponse{Text: strings.Repeat("界", 3000)})
	}))
	defer server.Close()
	text, err := (RemoteClient{Endpoint: server.URL}).Call(context.Background(), "query", map[string]any{"question": "x", "budget": 200})
	if err != nil || len(text) > 800 {
		t.Fatalf("budget: %d %v", len(text), err)
	}
}
