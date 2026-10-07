package core

import (
	"context"
	"encoding/json"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
)

func (c *Core) prepareCLIHostTLS(ctx context.Context, cfg *EffectiveExecutionConfig) error {
	if cfg.CLI != "praimate-code" {
		return nil
	}
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return err
	}
	endpoints := make([]string, 0, len(hosts)+1)
	for _, host := range hosts {
		endpoints = append(endpoints, host.Endpoint)
	}
	if cfg.Local != nil {
		endpoints = append(endpoints, cfg.Local.Endpoint)
	}
	global, err := launcher.LoadConfig()
	if err != nil {
		return err
	}
	if global != nil {
		endpoints = append(endpoints, global.DefaultLocalEndpoint)
	}
	trusted := map[string]string{}
	for _, endpoint := range endpoints {
		certificate, err := c.LocalHostTLSCertificate(ctx, endpoint)
		if err != nil {
			return err
		}
		if certificate == "" {
			continue
		}
		origin, err := hosttls.Origin(endpoint)
		if err != nil {
			return err
		}
		trusted[origin] = certificate
	}
	raw, err := json.Marshal(trusted)
	if err != nil {
		return err
	}
	if cfg.Env == nil {
		cfg.Env = map[string]string{}
	}
	// Also overwrite an inherited stale exception after consent is removed.
	cfg.Env["PRAIMATE_HOST_TLS"] = string(raw)
	return nil
}
