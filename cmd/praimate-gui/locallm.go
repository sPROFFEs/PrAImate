package main

// Local LLM tab bindings. Reads/writes local hosts and default endpoints
// in Core / launcher.Config, and probes models across all configured hosts.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"

	"github.com/sPROFFEs/PrAImate/internal/hosttls"
)

const localLLMAPIKeySetting = "local_llm.api_key"
const localLLMProbeTimeout = 5 * time.Second

// LocalHost describes an OpenAI-compatible host endpoint.
type LocalHost = core.LocalHost

// LocalHostOption bundles a host's info with its live probed models.
type LocalHostOption struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Endpoint  string   `json:"endpoint"`
	HasAPIKey bool     `json:"hasApiKey"`
	Models    []string `json:"models"`
	Error     string   `json:"error,omitempty"`
	IsDefault bool     `json:"isDefault"`
}

// LocalHostModelItem represents a model associated with its host.
type LocalHostModelItem struct {
	HostID   string `json:"hostId"`
	HostName string `json:"hostName"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// LocalLLMDefaults mirrors launcher.Config's DefaultLocal* slice.
type LocalLLMDefaults struct {
	Endpoint      string `json:"endpoint"`
	APIKey        string `json:"apiKey,omitempty"`
	HasAPIKey     bool   `json:"hasApiKey"`
	RemoveAPIKey  bool   `json:"removeApiKey,omitempty"`
	WireAPI       string `json:"wireApi"` // "", "responses", "chat"
	ContextTokens int    `json:"contextTokens"`
	OutputTokens  int    `json:"outputTokens"`
}

// ListLocalHosts returns all configured local LLM hosts.
func (a *App) ListLocalHosts() ([]LocalHost, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	hosts, err := c.ListLocalHosts(context.Background())
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		d, _ := a.GetLocalLLM()
		if d != nil && d.Endpoint != "" {
			hosts = []LocalHost{{
				ID:            "default",
				Name:          "Default Host",
				Endpoint:      d.Endpoint,
				HasAPIKey:     d.HasAPIKey,
				WireAPI:       d.WireAPI,
				ContextTokens: d.ContextTokens,
				OutputTokens:  d.OutputTokens,
				IsDefault:     true,
			}}
		}
	}
	for i := range hosts {
		if hosts[i].IsDefault {
			key, _ := loadLocalLLMAPIKey(c)
			hosts[i].HasAPIKey = key != ""
		} else {
			key, _ := loadHostAPIKey(c, hosts[i].ID)
			hosts[i].HasAPIKey = key != ""
		}
	}
	return hosts, nil
}

// SaveLocalHost creates or updates a local host configuration.
// Import the legacy global profile before using the shared Core host manager.
func (a *App) ensureCoreLocalHosts(c *core.Core) error {
	ctx := context.Background()
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil || len(hosts) > 0 {
		return err
	}
	legacy, err := a.ListLocalHosts()
	if err != nil {
		return err
	}
	for _, host := range legacy {
		host.APIKey, err = loadLocalLLMAPIKey(c)
		if err != nil {
			return err
		}
		if _, err := c.SaveLocalHost(ctx, host); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) SaveLocalHost(h LocalHost) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := a.ensureCoreLocalHosts(c); err != nil {
		return err
	}
	saved, err := c.SaveLocalHost(context.Background(), h)
	if err != nil {
		return err
	}
	if saved.IsDefault {
		return a.syncLocalHostDefault(saved)
	}
	return nil
}

func (a *App) syncLocalHostDefault(host *LocalHost) error {
	return a.SetLocalLLM(LocalLLMDefaults{Endpoint: host.Endpoint, WireAPI: host.WireAPI, ContextTokens: host.ContextTokens, OutputTokens: host.OutputTokens})
}

// DeleteLocalHost removes the host and promotes its successor with its own key.
func (a *App) DeleteLocalHost(id string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if err := a.ensureCoreLocalHosts(c); err != nil {
		return err
	}
	if err := c.DeleteLocalHost(context.Background(), id); err != nil {
		return err
	}
	hosts, err := c.ListLocalHosts(context.Background())
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].IsDefault {
			return a.syncLocalHostDefault(&hosts[i])
		}
	}
	return a.SetLocalLLM(LocalLLMDefaults{RemoveAPIKey: true})
}

func (a *App) SetDefaultLocalHost(id string) error {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return err
	}
	for _, host := range hosts {
		if host.ID == id {
			host.IsDefault = true
			return a.SaveLocalHost(host)
		}
	}
	return errors.New("local host not found")
}

// LocalLLMHostsModels probes models for all configured hosts.
func (a *App) LocalLLMHostsModels() ([]LocalHostOption, error) {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return nil, err
	}
	baseCtx := a.ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	type probeResult struct {
		index  int
		option *LocalHostOption
	}
	results := make(chan probeResult, len(hosts))
	for i, h := range hosts {
		go func(i int, h LocalHost) {
			ctx, cancel := context.WithTimeout(baseCtx, localLLMProbeTimeout)
			models, probeErr := a.core.NativeModels(ctx, h.Endpoint)
			cancel()
			if probeErr != nil {
				// Unreachable hosts are intentionally omitted. The saved host
				// remains in settings, but must not block or clutter pickers.
				results <- probeResult{index: i}
				return
			}
			results <- probeResult{index: i, option: &LocalHostOption{
				ID: h.ID, Name: h.Name, Endpoint: h.Endpoint,
				HasAPIKey: h.HasAPIKey, Models: models, IsDefault: h.IsDefault,
			}}
		}(i, h)
	}
	ordered := make([]*LocalHostOption, len(hosts))
	for range hosts {
		result := <-results
		ordered[result.index] = result.option
	}
	var options []LocalHostOption
	for _, option := range ordered {
		if option != nil {
			options = append(options, *option)
		}
	}
	return options, nil
}

func loadHostAPIKey(c *core.Core, hostID string) (string, error) {
	raw, err := c.GetSetting(context.Background(), core.ScopeCLI, "local_llm.host_key."+hostID)
	if err != nil || raw == nil {
		return "", err
	}
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", err
	}
	return key, nil
}

// GetLocalLLM returns the saved global default endpoint.
func (a *App) GetLocalLLM() (*LocalLLMDefaults, error) {
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return &LocalLLMDefaults{}, nil
	}
	apiKey := ""
	if a.core != nil {
		apiKey, err = loadLocalLLMAPIKey(a.core)
		if err != nil {
			return nil, err
		}
	} else {
		apiKey = cfg.DefaultLocalAPIKey
	}
	return &LocalLLMDefaults{
		Endpoint:      cfg.DefaultLocalEndpoint,
		HasAPIKey:     apiKey != "",
		WireAPI:       cfg.DefaultLocalWireAPI,
		ContextTokens: cfg.DefaultLocalContextTokens,
		OutputTokens:  cfg.DefaultLocalOutputTokens,
	}, nil
}

// SetLocalLLM persists the global default endpoint.
func (a *App) SetLocalLLM(d LocalLLMDefaults) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	cfg, err := launcher.LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil {
		cfg = &launcher.Config{}
	}
	cfg.DefaultLocalEndpoint = d.Endpoint
	cfg.DefaultLocalAPIKey = ""
	cfg.DefaultLocalWireAPI = d.WireAPI
	cfg.DefaultLocalContextTokens = d.ContextTokens
	cfg.DefaultLocalOutputTokens = d.OutputTokens
	switch {
	case d.RemoveAPIKey:
		if err := saveLocalLLMAPIKey(c, ""); err != nil {
			return err
		}
	case d.APIKey != "":
		if err := saveLocalLLMAPIKey(c, d.APIKey); err != nil {
			return err
		}
	}
	return launcher.SaveConfig(cfg)
}

func loadLocalLLMAPIKey(c *core.Core) (string, error) {
	raw, err := c.GetSetting(context.Background(), core.ScopeCLI, localLLMAPIKeySetting)
	if err != nil || raw == nil {
		return "", err
	}
	var key string
	if err := json.Unmarshal(raw, &key); err != nil {
		return "", err
	}
	return key, nil
}

func saveLocalLLMAPIKey(c *core.Core, key string) error {
	if c == nil {
		return errors.New("save local LLM API key: core unavailable")
	}
	// Keep an explicit empty value so the Core does not fall back to a
	// per-host backup of a credential the user has removed.
	raw, err := json.Marshal(key)
	if err != nil {
		return err
	}
	return c.SetSetting(context.Background(), core.ScopeCLI, localLLMAPIKeySetting, raw)
}

func redactChatCredential(chat *core.Chat) *core.Chat {
	if chat != nil && chat.Settings.Local != nil {
		local := *chat.Settings.Local
		local.APIKey = ""
		chat.Settings.Local = &local
	}
	return chat
}

func redactChatCredentials(chats []core.Chat) []core.Chat {
	for i := range chats {
		redactChatCredential(&chats[i])
	}
	return chats
}

func migrateLegacyLocalLLMAPIKey(c *core.Core, cfg *launcher.Config) error {
	if c == nil || cfg == nil || cfg.DefaultLocalAPIKey == "" {
		return nil
	}
	existing, err := loadLocalLLMAPIKey(c)
	if err != nil {
		return err
	}
	if existing == "" {
		if err := saveLocalLLMAPIKey(c, cfg.DefaultLocalAPIKey); err != nil {
			return err
		}
	}
	cfg.DefaultLocalAPIKey = ""
	return launcher.SaveConfig(cfg)
}

func (a *App) TestLocalLLM(endpoint, apiKey string) ([]string, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" {
		hosts, _ := a.ListLocalHosts()
		normalized := ollama.NormalizeEndpoint(endpoint)
		for _, h := range hosts {
			if ollama.NormalizeEndpoint(h.Endpoint) == normalized {
				if h.IsDefault {
					apiKey, _ = loadLocalLLMAPIKey(a.core)
				} else {
					apiKey, _ = loadHostAPIKey(a.core, h.ID)
				}
				break
			}
		}
		if apiKey == "" {
			apiKey, _ = loadLocalLLMAPIKey(a.core)
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	ctx, err = c.LocalHostTLSContext(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return ollama.ListCanonicalModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
}

// TestLocalHost probes a specific host using its stored or supplied credentials.
func (a *App) TestLocalHost(hostID, endpoint, apiKey string) ([]string, error) {
	c, err := a.requireCore()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(apiKey) == "" && hostID != "" {
		hosts, _ := a.ListLocalHosts()
		for _, h := range hosts {
			if h.ID == hostID {
				if h.IsDefault {
					apiKey, _ = loadLocalLLMAPIKey(a.core)
				} else {
					apiKey, _ = loadHostAPIKey(a.core, h.ID)
				}
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
	defer cancel()
	ctx, err = c.LocalHostTLSContext(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return ollama.ListCanonicalModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
}

func (a *App) InspectLocalHostCertificate(endpoint string) (*hosttls.Certificate, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return hosttls.Inspect(ctx, endpoint)
}

func (a *App) LocalHostCertificateTrusted(endpoint string) (bool, error) {
	c, err := a.requireCore()
	if err != nil {
		return false, err
	}
	certificate, err := c.LocalHostTLSCertificate(context.Background(), endpoint)
	return certificate != "", err
}

func (a *App) TrustLocalHostCertificate(endpoint, certificate string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.TrustLocalHostCertificate(context.Background(), endpoint, certificate)
}

func (a *App) RemoveLocalHostCertificate(endpoint string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	return c.RemoveLocalHostCertificate(context.Background(), endpoint)
}

// LocalLLMOption bundles the saved default endpoint with its live model list
// and all host options.
type LocalLLMOption struct {
	Configured bool                 `json:"configured"`
	Endpoint   string               `json:"endpoint"`
	HasAPIKey  bool                 `json:"hasApiKey"`
	WireAPI    string               `json:"wireApi"`
	Models     []string             `json:"models"`
	Hosts      []LocalHostOption    `json:"hosts,omitempty"`
	AllModels  []LocalHostModelItem `json:"allModels,omitempty"`
	Error      string               `json:"error,omitempty"`
}

// LocalLLMModels returns the configured hosts and models for pickers.
func (a *App) LocalLLMModels() (*LocalLLMOption, error) {
	d, err := a.GetLocalLLM()
	if err != nil {
		return nil, err
	}
	opt := &LocalLLMOption{
		Endpoint:  d.Endpoint,
		HasAPIKey: d.HasAPIKey,
		WireAPI:   d.WireAPI,
	}
	hostsOptions, _ := a.LocalLLMHostsModels()
	opt.Hosts = hostsOptions
	var allItems []LocalHostModelItem
	for _, h := range hostsOptions {
		for _, m := range h.Models {
			allItems = append(allItems, LocalHostModelItem{
				HostID:   h.ID,
				HostName: h.Name,
				Endpoint: h.Endpoint,
				Model:    m,
			})
		}
	}
	opt.AllModels = allItems

	// Only responsive hosts are returned to the pickers. Prefer the saved
	// default when it answered; otherwise use the first responsive host.
	for _, host := range hostsOptions {
		if d.Endpoint != "" && ollama.NormalizeEndpoint(host.Endpoint) == ollama.NormalizeEndpoint(d.Endpoint) {
			opt.Configured = true
			opt.Endpoint = host.Endpoint
			opt.HasAPIKey = host.HasAPIKey
			opt.Models = host.Models
			return opt, nil
		}
	}
	if len(hostsOptions) > 0 {
		opt.Configured = true
		opt.Endpoint = hostsOptions[0].Endpoint
		opt.HasAPIKey = hostsOptions[0].HasAPIKey
		opt.Models = hostsOptions[0].Models
	}
	return opt, nil
}
