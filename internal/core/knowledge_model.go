package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/knowledge"
)

func (c *Core) enrichAgentKnowledge(ctx context.Context, a *Agent, dir string, progress func(int, int)) error {
	config := a.KnowledgeConfig
	if config == nil || config.EnrichmentCLI == "" {
		return nil
	}
	workspace, err := os.MkdirTemp("", "praimate-knowledge-model-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	call := func(ctx context.Context, prompt string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		usage, err := c.BeginUsage(ctx, config.EnrichmentCLI, config.EnrichmentModel, "knowledge")
		if err != nil {
			return "", err
		}
		var text string
		var runErr error
		if config.EnrichmentCLI == "local" || config.EnrichmentCLI == "praimate-cli" {
			route, err := c.ResolveNativeWorkerRoute(ctx, config.EnrichmentEndpoint, config.EnrichmentModel)
			if err != nil {
				runErr = err
			} else {
				result, err := ExecuteNativeWorkerStream(ctx, *route, "Return only source-grounded semantic JSON. Never execute tools.", prompt, nil)
				runErr = err
				if result != nil {
					text = result.Content
					if result.Usage != nil {
						usage.Observe(StreamEvent{Type: "usage", Usage: result.Usage})
					}
				}
			}
		} else {
			adapter, err := GetCLIAdapter(config.EnrichmentCLI)
			if err != nil {
				runErr = err
			} else if !supportsManagedSafeMode(adapter) {
				runErr = errors.New("enrichment CLI must support safe, read-only execution")
			} else {
				opts := SingleShotOpts{Cwd: workspace, Model: config.EnrichmentModel, SystemPrompt: "Extract reference metadata only. Return exactly the requested JSON. No tools or commands.", Message: prompt}
				var result *Reply
				var err error
				if stream, ok := adapter.(interface {
					SingleShotStream(context.Context, SingleShotOpts, StreamHandler) (*Reply, error)
				}); ok {
					result, err = stream.SingleShotStream(ctx, opts, func(event StreamEvent) { usage.Observe(event) })
					if errors.Is(err, ErrStreamUnsupported) {
						result, err = adapter.SingleShot(ctx, opts)
					}
				} else {
					result, err = adapter.SingleShot(ctx, opts)
				}
				runErr = err
				if result != nil {
					text = result.Text
					if result.ExitCode != 0 && runErr == nil {
						runErr = fmt.Errorf("enrichment CLI exited with code %d", result.ExitCode)
					}
				}
			}
		}
		return text, errors.Join(runErr, usage.Finish(runErr))
	}
	_, err = knowledge.Enrich(ctx, dir, config.EnrichmentCLI+"@"+config.EnrichmentEndpoint, config.EnrichmentModel, config.MaxEnrichmentChunks, call, progress)
	return err
}

// Explicit host/API calls share the same per-agent source selection as tools.
func (c *Core) AgentKnowledgeRequest(ctx context.Context, id, action string, args map[string]any) (string, error) {
	a, err := c.GetAgent(ctx, id)
	if err != nil {
		return "", err
	}
	if a.Knowledge == "" {
		return "", errors.New("agent knowledge is disabled")
	}
	if a.KnowledgeConfig.Remote() {
		return (knowledge.RemoteClient{Endpoint: a.KnowledgeConfig.Endpoint, APIKey: a.knowledgeAPIKey}).Call(ctx, action, args)
	}
	dir, err := AgentKnowledgeDir(id)
	if err != nil {
		return "", err
	}
	if action != "query" {
		return "", errors.New("local explicit requests currently support query")
	}
	question, _ := args["question"].(string)
	budget, _ := args["budget"].(int)
	if budget == 0 {
		budget = 1200
	}
	return knowledge.Query(ctx, dir, question, budget)
}
