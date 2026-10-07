package core

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

func TestLocalHostCertificateSupportsDiscoveryNativeRequestsAndRevocation(t *testing.T) {
	c := newMemCore(t)
	ctx := context.Background()
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("host credential missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer s.Close()
	if _, err := c.SaveLocalHost(ctx, LocalHost{ID: "tls", Endpoint: s.URL, APIKey: "fixture-key"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TestLocalHost(ctx, "tls", s.URL, ""); err == nil {
		t.Fatal("untrusted host connected")
	}
	certificate, err := hosttls.Inspect(ctx, s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TrustLocalHostCertificate(ctx, s.URL, certificate.PEM); err != nil {
		t.Fatal(err)
	}
	models, err := c.TestLocalHost(ctx, "tls", s.URL, "")
	if err != nil || len(models) != 1 || models[0] != "test-model" {
		t.Fatalf("catalogue: %v %v", models, err)
	}
	saved, err := c.LocalHostTLSCertificate(ctx, s.URL+"/v1")
	if err != nil || saved != certificate.PEM {
		t.Fatal("trust did not persist across endpoint path normalization")
	}
	route, err := c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: s.URL, Model: "test-model", ContextTokens: 8192}, "test-model")
	if err != nil || route.TLSCertificate != saved {
		t.Fatalf("native route lost certificate: %v", err)
	}
	res, err := (nativeProvider{route: *route}).request(ctx, "POST", "/chat/completions", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if err := c.RemoveLocalHostCertificate(ctx, s.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TestLocalHost(ctx, "tls", s.URL, ""); err == nil {
		t.Fatal("revocation did not restore normal verification")
	}
}
