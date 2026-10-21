package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/assistant"
	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

func TestAssistantUsesLocalHostCertificateAndRefreshesCachedTrust(t *testing.T) {
	app := assistantAppFixture(t)
	ctx := context.Background()
	var wantedKey atomic.Value
	wantedKey.Store("fixture-key")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+wantedKey.Load().(string) {
			http.Error(w, "fixture credential missing", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"fixture"}]}`))
		case "/v1/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"type":"final","message":"TLS works"}`}, "finish_reason": "stop"}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	if _, err := app.core.SaveLocalHost(ctx, core.LocalHost{ID: "assistant-tls", Endpoint: server.URL, APIKey: "fixture-key"}); err != nil {
		t.Fatal(err)
	}
	config := assistant.DefaultConfig()
	config.ModelID, config.Endpoint, config.Model = "existing", server.URL, "fixture"
	before, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := before.Health(ctx); err == nil {
		t.Fatal("untrusted Assistant endpoint connected")
	}
	cert, err := hosttls.Inspect(ctx, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.core.TrustLocalHostCertificate(ctx, server.URL, cert.Trust); err != nil {
		t.Fatal(err)
	}
	trusted, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := trusted.Health(ctx); err != nil {
		t.Fatalf("Assistant ignored saved TLS consent: %v", err)
	}
	if trusted == before {
		t.Fatal("cached provider did not refresh after accepting trust")
	}
	reply, err := trusted.Generate(ctx, assistant.Request{Config: config, Message: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assistant.ParseDecision(reply); err != nil {
		t.Fatal(err)
	}
	again, err := app.getAssistantProvider(ctx, config)
	if err != nil || again != trusted {
		t.Fatal("unchanged provider should be reused", err)
	}
	wantedKey.Store("fixture-key-2")
	if _, err := app.core.SaveLocalHost(ctx, core.LocalHost{ID: "assistant-tls", Endpoint: server.URL, APIKey: "fixture-key-2"}); err != nil {
		t.Fatal(err)
	}
	rotated, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if rotated == trusted {
		t.Fatal("updated host credentials did not refresh the provider")
	}
	if err := rotated.Health(ctx); err != nil {
		t.Fatalf("rotated host credential failed: %v", err)
	}
	if err := app.core.RemoveLocalHostCertificate(ctx, server.URL); err != nil {
		t.Fatal(err)
	}
	revoked, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := revoked.Health(ctx); err == nil {
		t.Fatal("cached Assistant provider ignored TLS revocation")
	}
}

func TestAssistantTrustsPrivateCAOnlyForItsOrigin(t *testing.T) {
	app := assistantAppFixture(t)
	ctx := context.Background()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{Subject: pkix.Name{CommonName: "Assistant fixture CA"}, SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey}}}
	server.StartTLS()
	defer server.Close()
	trust, _ := json.Marshal(hosttls.Trust{Authorities: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))})
	if err := app.core.TrustLocalHostCertificate(ctx, server.URL, string(trust)); err != nil {
		t.Fatal(err)
	}
	config := assistant.DefaultConfig()
	config.ModelID = "existing"
	config.Endpoint = server.URL
	config.Model = "fixture"
	provider, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Health(ctx); err != nil {
		t.Fatalf("saved private CA was ignored: %v", err)
	}
	defer provider.Stop(ctx)
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	other.TLS = server.TLS.Clone()
	other.StartTLS()
	defer other.Close()
	client := provider.(*assistant.HTTPProvider).Client
	if response, err := client.Get(other.URL); err == nil {
		response.Body.Close()
		t.Fatal("Assistant TLS consent leaked to another origin")
	}
	config.Endpoint = other.URL
	otherProvider, err := app.getAssistantProvider(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer otherProvider.Stop(ctx)
	if err := otherProvider.Health(ctx); err == nil {
		t.Fatal("changing endpoints reused the first origin's CA consent")
	}
}
