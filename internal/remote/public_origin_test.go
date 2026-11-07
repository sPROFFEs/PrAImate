package remote

import (
	"net/http/httptest"
	"testing"
)

func TestPublicTunnelOriginIsExactAndCanChangeWithoutRestart(t *testing.T) {
	g, err := New(Config{Token: "synthetic-admin-credential-at-least-32-characters"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, url := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/path"} {
		if err = g.SetPublicOrigin(url); err == nil {
			t.Fatalf("accepted %s", url)
		}
	}
	if err = g.SetPublicOrigin("https://workspace.example.com"); err != nil {
		t.Fatal(err)
	}
	check := func(origin string, want int) {
		t.Helper()
		req := httptest.NewRequest("GET", "http://127.0.0.1:8844/v1/discovery", nil)
		req.Header.Set("Origin", origin)
		response := httptest.NewRecorder()
		g.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("origin %s: %d", origin, response.Code)
		}
	}
	check("https://workspace.example.com", 200)
	check("https://workspace.example.com.attacker.test", 403)
	_ = g.SetPublicOrigin("https://new.example.com")
	check("https://workspace.example.com", 403)
	check("https://new.example.com", 200)
}
