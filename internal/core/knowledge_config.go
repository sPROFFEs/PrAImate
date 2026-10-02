package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/knowledge"
)

// Configuration is portable; credentials stay in protected host settings.
type AgentKnowledgeConfig struct {
	Source              string `json:"source" yaml:"source"`
	Endpoint            string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	EnrichmentCLI       string `json:"enrichment_cli,omitempty" yaml:"enrichment_cli,omitempty"`
	EnrichmentModel     string `json:"enrichment_model,omitempty" yaml:"enrichment_model,omitempty"`
	EnrichmentEndpoint  string `json:"enrichment_endpoint,omitempty" yaml:"enrichment_endpoint,omitempty"`
	MaxEnrichmentChunks int    `json:"max_enrichment_chunks,omitempty" yaml:"max_enrichment_chunks,omitempty"`
}

func (k *AgentKnowledgeConfig) Remote() bool { return k != nil && k.Source == "remote" }
func (a *Agent) HasKnowledgeAPIKey() bool    { return a.knowledgeAPIKey != "" }
func (k *AgentKnowledgeConfig) Validate() error {
	if k == nil {
		return nil
	}
	if k.Source != "" && k.Source != "local" && k.Source != "remote" {
		return errors.New("source must be local or remote")
	}
	if k.Remote() || k.Endpoint != "" {
		if err := knowledge.ValidateRemoteEndpoint(k.Endpoint); err != nil {
			return err
		}
	}
	if k.EnrichmentCLI != "" && k.EnrichmentCLI != "local" && !isKnownCLI(k.EnrichmentCLI) {
		return errors.New("unknown enrichment CLI")
	}
	if k.EnrichmentCLI != "" && strings.TrimSpace(k.EnrichmentModel) == "" {
		return errors.New("enrichment requires a model")
	}
	if k.EnrichmentCLI == "local" && k.EnrichmentEndpoint == "" {
		return errors.New("local enrichment requires an endpoint")
	}
	if k.EnrichmentEndpoint != "" {
		if err := knowledge.ValidateRemoteEndpoint(k.EnrichmentEndpoint); err != nil {
			return err
		}
	}
	if k.MaxEnrichmentChunks < 0 || k.MaxEnrichmentChunks > 512 {
		return errors.New("enrichment limit must be between 1 and 512 (0 uses 64)")
	}
	if len(k.EnrichmentModel) > 512 || len(k.EnrichmentCLI) > 128 {
		return errors.New("enrichment configuration exceeds limits")
	}
	return nil
}

func knowledgeConfigKey(id string) string     { return "agent.knowledge:" + id }
func knowledgeAPIKeySetting(id string) string { return "agent.knowledge.key:" + id }

// An optional mode saves activation and configuration in the same transaction.
func (c *Core) SaveAgentKnowledgeConfig(ctx context.Context, id string, config AgentKnowledgeConfig, apiKey string, removeKey bool, mode ...string) (*Agent, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if len(apiKey) > 8192 || strings.ContainsAny(apiKey, "\r\n\x00") {
		return nil, errors.New("invalid knowledge API key")
	}
	if len(mode) > 1 || (len(mode) == 1 && mode[0] != "" && mode[0] != "raw" && mode[0] != "rag") {
		return nil, errors.New("knowledge mode must be disabled, raw or rag")
	}
	a, err := c.GetAgent(ctx, id)
	if err != nil {
		return nil, err
	}
	a.KnowledgeConfig = &config
	if len(mode) == 1 {
		a.Knowledge = mode[0]
	}
	if removeKey {
		apiKey = ""
		a.knowledgeKeyWrite = &apiKey
	} else if apiKey != "" {
		a.knowledgeKeyWrite = &apiKey
	}
	return c.upsertAgent(ctx, a)
}

func (c *Core) loadAgentKnowledgeConfig(ctx context.Context, a *Agent) error {
	raw, err := c.GetSetting(ctx, ScopeCLI, knowledgeConfigKey(a.ID))
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &a.KnowledgeConfig); err != nil {
			return err
		}
	}
	if !a.KnowledgeConfig.Remote() {
		return nil
	}
	raw, err = c.GetSetting(ctx, ScopeCLI, knowledgeAPIKeySetting(a.ID))
	if err != nil {
		return err
	}
	if len(raw) > 0 {
		return json.Unmarshal(raw, &a.knowledgeAPIKey)
	}
	return nil
}

func (c *Core) TestAgentKnowledgeRemote(ctx context.Context, id string) error {
	a, err := c.GetAgent(ctx, id)
	if err != nil {
		return err
	}
	if !a.KnowledgeConfig.Remote() {
		return errors.New("remote knowledge is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err = (knowledge.RemoteClient{Endpoint: a.KnowledgeConfig.Endpoint, APIKey: a.knowledgeAPIKey}).Call(ctx, "health", nil)
	return err
}
