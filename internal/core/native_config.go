package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
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
	if route.ContextTokens != 0 {
		route.ContextSource = "chat override"
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
			if route.ContextTokens == 0 && host.ContextTokens != 0 {
				route.ContextTokens = host.ContextTokens
				route.ContextSource = "host settings"
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
				if route.ContextTokens == 0 && global.DefaultLocalContextTokens != 0 {
					route.ContextTokens = global.DefaultLocalContextTokens
					route.ContextSource = "host settings"
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
		route.ContextTokens, route.ContextSource = c.nativeLimits.lookup(ctx, route)
	}
	route.OutputAutomatic = route.OutputTokens == 0
	if route.OutputAutomatic {
		route.OutputTokens = autoOutputTokens(route.ContextTokens, 0)
	}
	if route.OutputTokens < 1 || route.ContextTokens < 2048 || route.ContextTokens > 2_000_000 || route.OutputTokens >= route.ContextTokens {
		return nil, errors.New("invalid native context/output token limits")
	}
	if route.ContextTokens-route.OutputTokens-max(256, route.ContextTokens/20) < 256 {
		return nil, errors.New("output and safety reserves must leave at least 256 tokens for input; reduce the output reserve or increase the supported context window")
	}
	return &route, nil
}

func autoContextWindow(_ string, requested int) int {
	if requested >= 2048 {
		return requested
	}
	// A model's trained maximum does not describe the server's loaded window.
	return 8192
}

func autoOutputTokens(contextTokens, requested int) int {
	if requested > 0 && requested < contextTokens {
		return requested
	}
	out := contextTokens / 8
	if out < 1024 {
		out = 1024
	}
	if out > 4096 {
		out = 4096
	}
	if out >= contextTokens {
		out = contextTokens / 2
	}
	return out
}

// ResolveNativeWorkerRoute selects a saved local model route for a stateless
// worker. Credentials stay in the Core and are never stored in worker profiles.
func (c *Core) ResolveNativeWorkerRoute(ctx context.Context, endpoint, model string) (*ChatLocalEndpoint, error) {
	return c.resolveNativeRoute(ctx, &ChatLocalEndpoint{Endpoint: endpoint, Model: model}, model)
}

// SetNativeChatLimits overrides the context and output windows for one native
// chat. Zero inherits the selected host's values (or the core defaults).
func (c *Core) SetNativeChatLimits(ctx context.Context, chatID string, contextTokens, outputTokens int) error {
	if contextTokens < 0 || contextTokens > 2_000_000 || (contextTokens > 0 && contextTokens < 2048) {
		return errors.New("native context window must be 0 or 2048 to 2000000 tokens")
	}
	if outputTokens < 0 || outputTokens > 2_000_000 || (contextTokens > 0 && outputTokens >= contextTokens) {
		return errors.New("native output reserve must be less than the context window")
	}
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return err
	}
	if chat.CLIAgent != "praimate-cli" {
		return errors.New("context window override is only available for praimate-cli chats")
	}
	route := ChatLocalEndpoint{}
	if chat.Settings.Local != nil {
		route = *chat.Settings.Local
	}
	route.ContextTokens, route.OutputTokens = contextTokens, outputTokens
	if _, err := c.resolveNativeRoute(ctx, &route, chat.Settings.Model); err != nil {
		return err
	}
	return c.UpdateChatSettings(ctx, chatID, func(settings *ChatSettings) {
		if settings.Local == nil {
			settings.Local = &ChatLocalEndpoint{}
		}
		settings.Local.ContextTokens = contextTokens
		settings.Local.OutputTokens = outputTokens
	})
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
