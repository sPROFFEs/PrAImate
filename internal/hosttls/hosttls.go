// Package hosttls adds explicitly trusted certificates to one HTTPS origin.
package hosttls

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Certificate struct {
	Origin      string    `json:"origin"`
	PEM         string    `json:"pem"`
	Fingerprint string    `json:"fingerprint"`
	Expires     time.Time `json:"expires"`
	Trust       string    `json:"trust"`
	Authorities []string  `json:"authorities,omitempty"`
}

func Origin(endpoint string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("certificate exceptions require an HTTPS endpoint without credentials")
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return "https://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), nil
}

func Validate(endpoint, certificate string) (*x509.Certificate, error) {
	if _, err := Origin(endpoint); err != nil {
		return nil, err
	}
	block, rest := pem.Decode([]byte(certificate))
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("expected one PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(strings.TrimSpace(endpoint))
	if err := cert.VerifyHostname(u.Hostname()); err != nil {
		return nil, err
	}
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return nil, errors.New("certificate is expired or not valid yet")
	}
	return cert, nil
}

// Inspect sends no HTTP request, API key or prompt. Verification is disabled
// solely to retrieve the certificate for the user's explicit trust decision.
func Inspect(ctx context.Context, endpoint string) (*Certificate, error) {
	origin, err := Origin(endpoint)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(origin)
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}}
	conn, err := dialer.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	peer := conn.(*tls.Conn).ConnectionState().PeerCertificates
	if len(peer) == 0 {
		return nil, errors.New("host presented no certificate")
	}
	encoded := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer[0].Raw}))
	cert, err := Validate(endpoint, encoded)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(cert.Raw)
	result := &Certificate{Origin: origin, PEM: encoded, Trust: encoded, Fingerprint: hex.EncodeToString(hash[:]), Expires: cert.NotAfter}
	trust := Trust{Certificate: encoded}
	for i := 1; i < len(peer); i++ {
		if err := peer[i-1].CheckSignatureFrom(peer[i]); err != nil {
			return nil, errors.New("host presented an invalid issuing CA chain")
		}
		trust.Authorities += string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer[i].Raw}))
		hash := sha256.Sum256(peer[i].Raw)
		result.Authorities = append(result.Authorities, hex.EncodeToString(hash[:]))
	}
	if trust.Authorities != "" {
		raw, _ := json.Marshal(trust)
		result.Trust = string(raw)
		if _, _, err := ParseTrust(endpoint, result.Trust); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type originTransport struct {
	origin          string
	trusted, normal *http.Transport
}

func (t originTransport) CloseIdleConnections() {
	t.trusted.CloseIdleConnections()
	t.normal.CloseIdleConnections()
}

func (t originTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	origin, _ := Origin(req.URL.String())
	if origin == t.origin {
		return t.trusted.RoundTrip(req)
	}
	return t.normal.RoundTrip(req)
}

func Client(endpoint, certificate string, timeout time.Duration) (*http.Client, error) {
	client := &http.Client{Timeout: timeout}
	if certificate == "" {
		return client, nil
	}
	trust, certs, err := ParseTrust(endpoint, certificate)
	if err != nil {
		return nil, err
	}
	origin, _ := Origin(endpoint)
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	for _, cert := range certs {
		roots.AddCert(cert)
	}
	normal := http.DefaultTransport.(*http.Transport).Clone()
	trusted := normal.Clone()
	trusted.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, VerifyConnection: func(state tls.ConnectionState) error {
		if trust.Certificate != "" && (len(state.PeerCertificates) == 0 || !bytes.Equal(state.PeerCertificates[0].Raw, certs[0].Raw)) {
			return errors.New("trusted host certificate changed; review the new certificate")
		}
		return nil
	}}
	client.Transport = originTransport{origin: origin, trusted: trusted, normal: normal}
	client.CheckRedirect = func(req *http.Request, _ []*http.Request) error {
		redirectOrigin, _ := Origin(req.URL.String())
		if redirectOrigin != origin {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return client, nil
}

type contextKey struct{}
type scopedCertificate struct{ origin, pem string }

func WithCertificate(ctx context.Context, endpoint, certificate string) context.Context {
	origin, _ := Origin(endpoint)
	return context.WithValue(ctx, contextKey{}, scopedCertificate{origin, certificate})
}
func ContextClient(ctx context.Context, endpoint string, timeout time.Duration) (*http.Client, error) {
	certificate, _ := ctx.Value(contextKey{}).(scopedCertificate)
	origin, _ := Origin(endpoint)
	if origin != certificate.origin {
		certificate.pem = ""
	}
	return Client(endpoint, certificate.pem, timeout)
}
