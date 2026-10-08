package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

func localTLSKey(endpoint string) (string, error) {
	origin, err := hosttls.Origin(endpoint)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(origin))
	return "local_llm.tls_certificate." + hex.EncodeToString(hash[:]), nil
}

func (c *Core) LocalHostTLSCertificate(ctx context.Context, endpoint string) (string, error) {
	key, err := localTLSKey(endpoint)
	if err != nil {
		return "", nil
	} // ordinary HTTP hosts have no TLS exception
	raw, err := c.GetSetting(ctx, ScopeCLI, key)
	if err != nil || len(raw) == 0 {
		return "", err
	}
	var certificate string
	err = json.Unmarshal(raw, &certificate)
	return certificate, err
}

func (c *Core) TrustLocalHostCertificate(ctx context.Context, endpoint, certificate string) error {
	if _, _, err := hosttls.ParseTrust(endpoint, certificate); err != nil {
		return err
	}
	key, err := localTLSKey(endpoint)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(certificate)
	if err != nil {
		return err
	}
	return c.SetSetting(ctx, ScopeCLI, key, raw)
}

func (c *Core) RemoveLocalHostCertificate(ctx context.Context, endpoint string) error {
	key, err := localTLSKey(endpoint)
	if err != nil {
		return err
	}
	return c.DeleteSetting(ctx, ScopeCLI, key)
}

func (c *Core) LocalHostTLSContext(ctx context.Context, endpoint string) (context.Context, error) {
	certificate, err := c.LocalHostTLSCertificate(ctx, endpoint)
	return hosttls.WithCertificate(ctx, endpoint, certificate), err
}
