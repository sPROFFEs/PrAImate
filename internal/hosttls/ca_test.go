package hosttls

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
	"testing"
	"time"
)

func TestPrivateCAChainAndExplicitAuthorityTrust(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{Subject: pkix.Name{CommonName: "PrAImate fixture CA"}, SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, _ := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	ca, _ = x509.ParseCertificate(rootDER)
	leafKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	leaf := &x509.Certificate{Subject: pkix.Name{CommonName: "PrAImate fixture endpoint"}, SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, _ := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey}}}
	server.StartTLS()
	defer server.Close()
	inspected, err := Inspect(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(inspected)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["trust"] == nil {
		t.Fatal("inspection lost the issuing CA chain")
	}
	rootPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
	material, _ := json.Marshal(map[string]string{"authorities": rootPEM})
	for _, trust := range []string{fields["trust"].(string), string(material)} {
		client, err := Client(server.URL, trust, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		res, err := client.Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		client.CloseIdleConnections()
	}
	bad, _ := json.Marshal(map[string]string{"authorities": inspected.PEM})
	if _, err := Client(server.URL, string(bad), time.Second); err == nil {
		t.Fatal("non-CA accepted as an authority")
	}
	bad, _ = json.Marshal(map[string]string{"authorities": rootPEM + "-----BEGIN PRIVATE KEY-----\nfixture\n-----END PRIVATE KEY-----\n"})
	if _, err := Client(server.URL, string(bad), time.Second); err == nil {
		t.Fatal("private key accepted in a public CA bundle")
	}
	other := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	other.TLS = server.TLS.Clone()
	other.StartTLS()
	defer other.Close()
	client, err := Client(server.URL, string(material), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	if res, err := client.Get(other.URL); err == nil {
		res.Body.Close()
		t.Fatal("CA consent leaked to another origin")
	}
}
