package runtime

import (
	"context"
	"errors"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

// Native executes a stateless, tool-free task through the existing
// OpenAI-compatible transport. The route is supplied by trusted host config.
type Native struct {
	Route           core.ChatLocalEndpoint
	AutomaticOutput bool // only configured routes with no explicit worker limit
}

func (Native) ID() string { return "native" }

func (Native) Capabilities() Capabilities {
	return Capabilities{OutputTokenLimit: true, ProviderUsage: true, ReadOnly: true}
}

func (n Native) Execute(ctx context.Context, req Request) (*Result, error) {
	if err := validate(req); err != nil {
		return nil, err
	}
	if req.Limits.MaxOutputTokens <= 0 {
		return nil, errors.New("native worker requires a positive output token limit")
	}
	ctx, cancel := boundedContext(ctx, req.Limits.Timeout)
	defer cancel()
	route := n.Route
	route.Model = req.Model
	route.OutputTokens = req.Limits.MaxOutputTokens
	route.OutputAutomatic = n.AutomaticOutput
	var emit core.StreamHandler
	if req.Progress != nil {
		emit = func(event core.StreamEvent) {
			if event.Type == "text" && event.Text != "" {
				req.Progress(ProgressEvent{Kind: "stream", Text: event.Text})
			}
		}
	}
	reply, err := core.ExecuteNativeWorkerStream(ctx, route, req.SystemPrompt, req.Task, emit)
	if err != nil {
		return nil, err
	}
	result := &Result{Content: reply.Content, FinishReason: "stop", Usage: Usage{Source: "unavailable"}}
	if reply.Usage != nil {
		result.Usage = Usage{InputTokens: reply.Usage.PromptTokens, OutputTokens: reply.Usage.CompletionTokens, Source: "provider"}
	}
	return result, nil
}
