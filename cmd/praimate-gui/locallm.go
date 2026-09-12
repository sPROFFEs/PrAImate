package main

// Local LLM tab bindings. Reads/writes local hosts and default endpoints
// in Core / launcher.Config, and probes models across all configured hosts.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/core"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

const localLLMAPIKeySetting = "local_llm.api_key"
const localLLMHostsSetting = "local_llm.hosts"

// LocalHost describes an OpenAI-compatible host endpoint.
type LocalHost struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Endpoint      string   `json:"endpoint"`
	APIKey        string   `json:"apiKey,omitempty"`
	HasAPIKey     bool     `json:"hasApiKey"`
	RemoveAPIKey  bool     `json:"removeApiKey,omitempty"`
	WireAPI       string   `json:"wireApi,omitempty"`
	ContextTokens int      `json:"contextTokens"`
	OutputTokens  int      `json:"outputTokens"`
	IsDefault     bool     `json:"isDefault"`
	ActiveModels  []string `json:"activeModels,omitempty"`
}

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
	raw, err := c.GetSetting(context.Background(), core.ScopeCLI, localLLMHostsSetting)
	var hosts []LocalHost
	if err == nil && raw != nil {
		_ = json.Unmarshal(raw, &hosts)
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
func (a *App) SaveLocalHost(h LocalHost) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	if strings.TrimSpace(h.Endpoint) == "" {
		return errors.New("endpoint URL is required")
	}
	if strings.TrimSpace(h.Name) == "" {
		h.Name = h.Endpoint
	}
	hosts, _ := a.ListLocalHosts()
	if h.ID == "" {
		h.ID = "host_" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	if len(hosts) == 0 {
		h.IsDefault = true
	}
	found := false
	for i := range hosts {
		if hosts[i].ID == h.ID {
			if h.IsDefault {
				for j := range hosts {
					hosts[j].IsDefault = false
				}
			}
			hosts[i] = h
			found = true
			break
		}
	}
	if !found {
		if h.IsDefault {
			for j := range hosts {
				hosts[j].IsDefault = false
			}
		}
		hosts = append(hosts, h)
	}
	if h.IsDefault {
		_ = a.SetLocalLLM(LocalLLMDefaults{
			Endpoint:      h.Endpoint,
			APIKey:        h.APIKey,
			RemoveAPIKey:  h.RemoveAPIKey,
			WireAPI:       h.WireAPI,
			ContextTokens: h.ContextTokens,
			OutputTokens:  h.OutputTokens,
		})
	} else {
		if h.RemoveAPIKey {
			_ = saveHostAPIKey(c, h.ID, "")
		} else if h.APIKey != "" {
			_ = saveHostAPIKey(c, h.ID, h.APIKey)
		}
	}
	raw, err := json.Marshal(hosts)
	if err != nil {
		return err
	}
	return c.SetSetting(context.Background(), core.ScopeCLI, localLLMHostsSetting, raw)
}

// DeleteLocalHost removes a local host configuration.
func (a *App) DeleteLocalHost(id string) error {
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	hosts, _ := a.ListLocalHosts()
	var next []LocalHost
	for _, h := range hosts {
		if h.ID != id {
			next = append(next, h)
		} else {
			_ = saveHostAPIKey(c, h.ID, "")
		}
	}
	hasDef := false
	for _, h := range next {
		if h.IsDefault {
			hasDef = true
			break
		}
	}
	if len(next) > 0 && !hasDef {
		next[0].IsDefault = true
		_ = a.SetLocalLLM(LocalLLMDefaults{
			Endpoint:      next[0].Endpoint,
			WireAPI:       next[0].WireAPI,
			ContextTokens: next[0].ContextTokens,
			OutputTokens:  next[0].OutputTokens,
		})
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return c.SetSetting(context.Background(), core.ScopeCLI, localLLMHostsSetting, raw)
}

// SetDefaultLocalHost sets a host as the primary default host.
func (a *App) SetDefaultLocalHost(id string) error {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return err
	}
	for i := range hosts {
		if hosts[i].ID == id {
			hosts[i].IsDefault = true
			_ = a.SetLocalLLM(LocalLLMDefaults{
				Endpoint:      hosts[i].Endpoint,
				WireAPI:       hosts[i].WireAPI,
				ContextTokens: hosts[i].ContextTokens,
				OutputTokens:  hosts[i].OutputTokens,
			})
		} else {
			hosts[i].IsDefault = false
		}
	}
	c, err := a.requireCore()
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(hosts)
	return c.SetSetting(context.Background(), core.ScopeCLI, localLLMHostsSetting, raw)
}

// LocalLLMHostsModels probes models for all configured hosts.
func (a *App) LocalLLMHostsModels() ([]LocalHostOption, error) {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return nil, err
	}
	var options []LocalHostOption
	for _, h := range hosts {
		opt := LocalHostOption{
			ID:        h.ID,
			Name:      h.Name,
			Endpoint:  h.Endpoint,
			HasAPIKey: h.HasAPIKey,
			IsDefault: h.IsDefault,
		}
		var apiKey string
		if h.IsDefault {
			apiKey, _ = loadLocalLLMAPIKey(a.core)
		} else {
			apiKey, _ = loadHostAPIKey(a.core, h.ID)
		}
		ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
		models, err := ollama.ListModels(ctx, ollama.NormalizeEndpoint(h.Endpoint), apiKey)
		cancel()
		if err != nil {
			opt.Error = err.Error()
		} else {
			opt.Models = models
		}
		options = append(options, opt)
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

func saveHostAPIKey(c *core.Core, hostID, key string) error {
	if c == nil {
		return errors.New("save host API key: core unavailable")
	}
	if key == "" {
		return c.DeleteSetting(context.Background(), core.ScopeCLI, "local_llm.host_key."+hostID)
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return err
	}
	return c.SetSetting(context.Background(), core.ScopeCLI, "local_llm.host_key."+hostID, raw)
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
	if key == "" {
		return c.DeleteSetting(context.Background(), core.ScopeCLI, localLLMAPIKeySetting)
	}
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
	return ollama.ListModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
}

// TestLocalHost probes a specific host using its stored or supplied credentials.
func (a *App) TestLocalHost(hostID, endpoint, apiKey string) ([]string, error) {
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
	return ollama.ListModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
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
		Configured: d.Endpoint != "",
		Endpoint:   d.Endpoint,
		HasAPIKey:  d.HasAPIKey,
		WireAPI:    d.WireAPI,
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

	if !opt.Configured {
		if len(hostsOptions) > 0 && hostsOptions[0].Endpoint != "" {
			opt.Configured = true
			opt.Endpoint = hostsOptions[0].Endpoint
			opt.HasAPIKey = hostsOptions[0].HasAPIKey
			opt.Models = hostsOptions[0].Models
			opt.Error = hostsOptions[0].Error
		}
		return opt, nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, 10*time.Second)
	defer cancel()
	apiKey, err := loadLocalLLMAPIKey(a.core)
	if err != nil {
		return nil, err
	}
	models, err := ollama.ListModels(ctx, ollama.NormalizeEndpoint(d.Endpoint), apiKey)
	if err != nil {
		opt.Error = err.Error()
		return opt, nil
	}
	opt.Models = models
	return opt, nil
}
