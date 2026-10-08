package hosttls

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"strings"
	"time"
)

// Trust preserves legacy leaf pins and supports explicitly imported public CAs.
type Trust struct {
	Certificate string `json:"certificate,omitempty"`
	Authorities string `json:"authorities,omitempty"`
}

func ParseTrust(endpoint, material string) (Trust, []*x509.Certificate, error) {
	if _, err := Origin(endpoint); err != nil {
		return Trust{}, nil, err
	}
	if len(material) > 128<<10 {
		return Trust{}, nil, errors.New("certificate bundle exceeds 128 KiB")
	}
	trust := Trust{Certificate: material}
	if strings.HasPrefix(strings.TrimSpace(material), "{") {
		trust = Trust{}
		decoder := json.NewDecoder(strings.NewReader(material))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&trust); err != nil {
			return Trust{}, nil, err
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return Trust{}, nil, errors.New("invalid certificate trust record")
		}
	}
	var certs []*x509.Certificate
	if trust.Certificate != "" {
		cert, err := Validate(endpoint, trust.Certificate)
		if err != nil {
			return Trust{}, nil, err
		}
		certs = append(certs, cert)
	}
	remaining := strings.TrimSpace(trust.Authorities)
	for remaining != "" {
		if !strings.HasPrefix(remaining, "-----BEGIN CERTIFICATE-----") {
			return Trust{}, nil, errors.New("CA bundle must contain public certificates only")
		}
		block, rest := pem.Decode([]byte(remaining))
		if block == nil || block.Type != "CERTIFICATE" {
			return Trust{}, nil, errors.New("invalid CA certificate")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return Trust{}, nil, err
		}
		if !cert.IsCA || (cert.KeyUsage != 0 && cert.KeyUsage&x509.KeyUsageCertSign == 0) {
			return Trust{}, nil, errors.New("certificate is not a signing CA")
		}
		if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return Trust{}, nil, errors.New("CA certificate is expired or not valid yet")
		}
		certs = append(certs, cert)
		if len(certs) > 16 {
			return Trust{}, nil, errors.New("CA bundle exceeds 16 certificates")
		}
		remaining = strings.TrimSpace(string(rest))
	}
	if len(certs) == 0 {
		return Trust{}, nil, errors.New("empty certificate trust record")
	}
	return trust, certs, nil
}

func PublicBundle(endpoint, material string) (string, error) {
	_, certs, err := ParseTrust(endpoint, material)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, cert := range certs {
		out.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	}
	return out.String(), nil
}
