package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

type nativeCredentialKey struct{}
type nativeCredential struct{ endpoint, key string }

// WithNativeAPIKey supplies an ephemeral credential for the selected route.
// It never writes a key to chat settings, argv, or project files.
func WithNativeAPIKey(ctx context.Context, endpoint, key string) context.Context {
	return context.WithValue(ctx, nativeCredentialKey{}, nativeCredential{endpoint, key})
}

func (c *Core) resolveNativeRoute(ctx context.Context, requested *ChatLocalEndpoint, model string) (*ChatLocalEndpoint, error) {
	route := ChatLocalEndpoint{}
	if requested != nil {
		route = *requested
	}
	global, err := launcher.LoadConfig()
	if err != nil {
		return nil, err
	}
	if route.Model == "" {
		route.Model = strings.TrimSpace(model)
	}
	if route.Model == "" {
		route.Model = strings.TrimSpace(os.Getenv("PRAIMATE_MODEL"))
	}
	if route.Model == "" {
		route.Model = strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	}
	if c.store != nil {
		assignments, err := c.NativeModelAssignments(ctx)
		if err != nil {
			return nil, err
		}
		selected, err := selectNativeAssignment(assignments, route.Endpoint, route.Model)
		if err != nil {
			return nil, err
		}
		if selected != nil {
			// A model qualified with another host must not carry credentials
			// supplied for the originally requested endpoint.
			if route.Endpoint != "" && ollama.NormalizeEndpoint(route.Endpoint) != ollama.NormalizeEndpoint(selected.Endpoint) {
				route.APIKey = ""
			}
			route.Endpoint = selected.Endpoint
			route.Model = selected.Model
		}
	}
	if route.Endpoint == "" {
		if global != nil && global.DefaultLocalEndpoint != "" {
			route.Endpoint = global.DefaultLocalEndpoint
		} else {
			route.Endpoint = os.Getenv("OPENAI_BASE_URL")
			if route.Endpoint == "" {
				route.Endpoint = os.Getenv("OPENAI_API_BASE")
			}
			if route.Endpoint == "" {
				route.Endpoint = "http://127.0.0.1:11434/v1"
			}
			route.APIKey = os.Getenv("OPENAI_API_KEY")
		}
	}
	route.Endpoint = ollama.NormalizeEndpoint(route.Endpoint)
	base, err := nativeBaseURL(route.Endpoint)
	if err != nil {
		return nil, err
	}
	route.Endpoint = base
	if credential, ok := ctx.Value(nativeCredentialKey{}).(nativeCredential); ok && credential.key != "" {
		selected, _ := nativeBaseURL(ollama.NormalizeEndpoint(credential.endpoint))
		if credential.endpoint == "" || selected == base {
			route.APIKey = credential.key
		}
	}
	// Resolve only the credential belonging to the selected endpoint.
	if c.store != nil {
		hosts, err := c.ListLocalHosts(ctx)
		if err != nil {
			return nil, err
		}
		for _, host := range hosts {
			hostBase, _ := nativeBaseURL(ollama.NormalizeEndpoint(host.Endpoint))
			if hostBase != base {
				continue
			}
			if route.APIKey == "" {
				route.APIKey, err = c.localHostSecret(ctx, host.ID, host.IsDefault)
				if err != nil {
					return nil, err
				}
			}
			if route.ContextTokens == 0 {
				route.ContextTokens = host.ContextTokens
			}
			if route.OutputTokens == 0 {
				route.OutputTokens = host.OutputTokens
			}
			break
		}
		if global != nil {
			savedBase, _ := nativeBaseURL(ollama.NormalizeEndpoint(global.DefaultLocalEndpoint))
			if savedBase == base {
				if route.APIKey == "" {
					route.APIKey, err = c.localLLMAPIKey(ctx)
					if err != nil {
						return nil, err
					}
				}
				if route.ContextTokens == 0 {
					route.ContextTokens = global.DefaultLocalContextTokens
				}
				if route.OutputTokens == 0 {
					route.OutputTokens = global.DefaultLocalOutputTokens
				}
			}
		}
	}
	if route.Model == "" {
		return nil, errors.New("PrAImate CLI requires a model: select one in Local LLM settings or pass --model")
	}
	if route.ContextTokens == 0 {
		route.ContextTokens = 8192
	}
	if route.OutputTokens == 0 {
		route.OutputTokens = 1024
	}
	if route.OutputTokens < 1 || route.ContextTokens < 2048 || route.ContextTokens > 2_000_000 || route.OutputTokens >= route.ContextTokens {
		return nil, errors.New("invalid native context/output token limits")
	}
	return &route, nil
}

func selectNativeAssignment(assignments []NativeModelAssignment, endpoint, model string) (*NativeModelAssignment, error) {
	qualified := strings.Contains(model, "::")
	var selected *NativeModelAssignment
	for i := range assignments {
		candidate := &assignments[i]
		if qualified {
			if model == candidate.HostID+"::"+candidate.Model {
				return candidate, nil
			}
			continue
		}
		if endpoint != "" && ollama.NormalizeEndpoint(endpoint) != ollama.NormalizeEndpoint(candidate.Endpoint) {
			continue
		}
		if model != "" {
			if model != candidate.Model {
				continue
			}
			if selected != nil {
				return nil, fmt.Errorf("model %q belongs to multiple local hosts; select HOST_ID::MODEL", model)
			}
			selected = candidate
			continue
		}
		if selected == nil || (candidate.IsDefault && !selected.IsDefault) {
			selected = candidate
		}
	}
	if qualified {
		return nil, fmt.Errorf("assigned model %q not found", model)
	}
	return selected, nil
}

// NativeModels uses the same stored endpoint and credentials as native runs.
func (c *Core) NativeModels(ctx context.Context, endpoint string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	route, err := c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: endpoint}, "catalog")
	if err != nil {
		return nil, err
	}
	return (nativeProvider{route: *route}).models(ctx)
}

func (c *Core) prepareNativeExecution(ctx context.Context, cfg *EffectiveExecutionConfig) error {
	if cfg.Local == nil {
		return errors.New("native execution requires an endpoint")
	}
	var servers []MCPServer
	var err error
	switch {
	case cfg.explicitMCP:
		servers, err = c.resolveMCPServerIDs(ctx, cfg.mcpServers)
	case cfg.Agent != nil:
		servers, err = c.resolveMCPServerIDs(ctx, cfg.Agent.MCPServers)
	case cfg.allEnabledMCP:
		servers, err = c.ListMCPServers(ctx, true)
	}
	if err != nil {
		return fmt.Errorf("native MCP selection: %w", err)
	}
	servers = append(servers, cfg.InternalMCPServers...)
	cfg.native = &nativeExecution{core: c, agent: cfg.Agent, local: *cfg.Local, servers: servers, approval: cfg.Approval}
	cfg.native.chatID = cfg.ChatID
	cfg.native.attachments = cfg.attachments
	if cfg.skillSettings != nil {
		cfg.native.settings = *cfg.skillSettings
	}
	cfg.native.settings.Local = cfg.Local
	return nil
}
