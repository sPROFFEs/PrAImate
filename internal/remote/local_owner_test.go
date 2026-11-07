package remote

import (
	"net/http/httptest"
	"testing"
)

func TestLocalOwnerCredentialKeepsBootstrapAuthenticationIndependent(t *testing.T) {
	local := "synthetic-local-owner-credential-for-test"
	g, err := New(Config{Token: testToken, LocalOwnerToken: local}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, credential := range []string{testToken, local} {
		req := httptest.NewRequest("GET", "/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+credential)
		principal, ok := g.authenticate(req)
		if !ok || principal.Role != "admin" {
			t.Fatal("expected administrator credential rejected")
		}
	}
	if g.config.LocalOwnerToken != "" {
		t.Fatal("raw local token was retained in gateway configuration")
	}
	fresh, err := New(Config{Token: testToken, LocalOwnerToken: local + "rotated"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer "+local)
	if _, ok := fresh.authenticate(req); ok {
		t.Fatal("old local owner credential survived a process restart")
	}
}
