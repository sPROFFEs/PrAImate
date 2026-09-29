package runtime

import (
	"context"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// PraimateCLI runs the in-process PrAImate model transport with the selected
// saved host/model assignment. The regular CLI adapter needs a prepared chat
// context, so independent workers resolve their own route through Core.
type PraimateCLI struct{ Core *core.Core }

func (PraimateCLI) ID() string { return "cli:praimate-cli" }
func (PraimateCLI) Capabilities() Capabilities {
	return Capabilities{ReadOnly: true, OutputTokenLimit: true, ProviderUsage: true}
}

func (p PraimateCLI) Execute(ctx context.Context, req Request) (*Result, error) {
	return (ConfiguredNative{Core: p.Core}).Execute(ctx, req)
}

// ConfiguredNative resolves endpoint credentials and model aliases from Core
// immediately before a worker request. This also supports named local hosts.
type ConfiguredNative struct {
	Core     *core.Core
	Endpoint string
}

func (ConfiguredNative) ID() string { return "native:configured" }
func (ConfiguredNative) Capabilities() Capabilities {
	return (Native{}).Capabilities()
}

func (p ConfiguredNative) Execute(ctx context.Context, req Request) (*Result, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	ctx, cancel := boundedContext(ctx, req.Limits.Timeout)
	defer cancel()
	if p.Core == nil {
		return nil, errors.New("configured native worker requires the PrAImate Core")
	}
	route, err := p.Core.ResolveNativeWorkerRoute(ctx, p.Endpoint, req.Model)
	if err != nil {
		return nil, err
	}
	if req.Limits.MaxOutputTokens <= 0 {
		req.Limits.MaxOutputTokens = route.OutputTokens
		if req.Limits.MaxOutputTokens <= 0 {
			req.Limits.MaxOutputTokens = 2048
		}
	}
	req.Model = route.Model
	return (Native{Route: *route}).Execute(ctx, req)
}
