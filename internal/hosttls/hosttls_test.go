package hosttls

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCertificateTrustIsExplicitAndScopedToOrigin(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer s.Close()
	ctx := context.Background()
	ordinary, _ := Client(s.URL, "", time.Second)
	if res, err := ordinary.Get(s.URL); err == nil {
		res.Body.Close()
		t.Fatal("untrusted certificate accepted by default")
	}
	certificate, err := Inspect(ctx, s.URL)
	if err != nil || certificate.Fingerprint == "" {
		t.Fatalf("inspection: %+v %v", certificate, err)
	}
	trusted, err := Client(s.URL, certificate.PEM, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer trusted.CloseIdleConnections()
	res, err := trusted.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer other.Close()
	if res, err := trusted.Get(other.URL); err == nil {
		res.Body.Close()
		t.Fatal("exception leaked to a different port/origin")
	}
	if _, err := Validate("https://wrong.example", certificate.PEM); err == nil {
		t.Fatal("hostname mismatch accepted")
	}
	if _, err := Inspect(ctx, "http://127.0.0.1"); err == nil {
		t.Fatal("HTTP certificate exception accepted")
	}
	if _, err := Validate(s.URL, certificate.PEM+certificate.PEM); err == nil {
		t.Fatal("multiple trust anchors accepted")
	}
}

func TestCertificateContextCannotLeakToAnotherOrigin(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	certificate, err := Inspect(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithCertificate(context.Background(), s.URL, certificate.PEM)
	client, err := ContextClient(ctx, "https://different.example", time.Second)
	if err != nil || client.Transport != nil {
		t.Fatal("scoped context applied trust to another host")
	}
}

func TestChangedCertificateRequiresNewApprovalEvenIfOldCertificateSignsIt(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	certificate, err := Inspect(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	key := s.TLS.Certificates[0].PrivateKey.(crypto.Signer)
	parent := s.Certificate()
	child := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: parent.IPAddresses, DNSNames: parent.DNSNames, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, child, parent, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	rotated := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	rotated.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der, parent.Raw}, PrivateKey: key}}}
	rotated.StartTLS()
	defer rotated.Close()
	client, err := Client(s.URL, certificate.PEM, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	// Simulate a server certificate rotation without changing the URL/origin.
	client.Transport.(originTransport).trusted.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, rotated.Listener.Addr().String())
	}
	if res, err := client.Get(s.URL); err == nil {
		res.Body.Close()
		t.Fatal("changed leaf certificate accepted")
	} else if !strings.Contains(err.Error(), "certificate changed") {
		t.Fatal(err)
	}
}
