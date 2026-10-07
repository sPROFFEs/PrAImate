package core

import (
	"context"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

// Never use the default host's key for an unrelated model endpoint.
func (c *Core) localEndpointCredential(ctx context.Context, endpoint string) (string, error) {
	key, _, err := c.localEndpointCredentialForRoute(ctx, endpoint)
	return key, err
}

func (c *Core) localEndpointCredentialForRoute(ctx context.Context, endpoint string) (string, bool, error) {
	base, err := nativeBaseURL(ollama.NormalizeEndpoint(endpoint))
	if err != nil {
		return "", false, err
	}
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return "", false, err
	}
	for _, host := range hosts {
		hostBase, _ := nativeBaseURL(ollama.NormalizeEndpoint(host.Endpoint))
		if base == hostBase {
			key, err := c.localHostSecret(ctx, host.ID, host.IsDefault)
			return key, true, err
		}
	}
	global, err := launcher.LoadConfig()
	if err != nil {
		return "", false, err
	}
	if global != nil {
		defaultBase, _ := nativeBaseURL(ollama.NormalizeEndpoint(global.DefaultLocalEndpoint))
		if base == defaultBase {
			key, err := c.localLLMAPIKey(ctx)
			return key, true, err
		}
	}
	return "", false, nil
}
