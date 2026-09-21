package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

const localHostsSetting = "local_llm.hosts"

// LocalHost is a reusable local-model profile. APIKey is accepted on writes
// but never returned by ListLocalHosts; HasAPIKey is the public indicator.
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

func localHostKey(id string) string { return "local_llm.host_key." + id }

func (c *Core) localHostSecret(ctx context.Context, id string, isDefault bool) (string, error) {
	key := localHostKey(id)
	if isDefault {
		key = "local_llm.api_key"
	}
	raw, err := c.GetSetting(ctx, ScopeCLI, key)
	if err != nil || len(raw) == 0 {
		return "", err
	}
	var secret string
	if err := json.Unmarshal(raw, &secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (c *Core) setLocalHostSecret(ctx context.Context, id string, isDefault bool, key string) error {
	setting := localHostKey(id)
	if isDefault {
		setting = "local_llm.api_key"
	}
	if key == "" {
		return c.DeleteSetting(ctx, ScopeCLI, setting)
	}
	raw, err := json.Marshal(key)
	if err != nil {
		return err
	}
	return c.SetSetting(ctx, ScopeCLI, setting, raw)
}

func (c *Core) ListLocalHosts(ctx context.Context) ([]LocalHost, error) {
	raw, err := c.GetSetting(ctx, ScopeCLI, localHostsSetting)
	if err != nil || len(raw) == 0 {
		return []LocalHost{}, err
	}
	var hosts []LocalHost
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return nil, err
	}
	for i := range hosts {
		key, err := c.localHostSecret(ctx, hosts[i].ID, hosts[i].IsDefault)
		if err != nil {
			return nil, err
		}
		hosts[i].APIKey = ""
		hosts[i].RemoveAPIKey = false
		hosts[i].HasAPIKey = key != ""
	}
	return hosts, nil
}

func (c *Core) SaveLocalHost(ctx context.Context, host LocalHost) (*LocalHost, error) {
	host.Endpoint = strings.TrimSpace(host.Endpoint)
	if host.Endpoint == "" {
		return nil, errors.New("local model endpoint is required")
	}
	if host.Name = strings.TrimSpace(host.Name); host.Name == "" {
		host.Name = host.Endpoint
	}
	if host.ID == "" {
		host.ID = fmt.Sprintf("host_%d", time.Now().UnixNano())
	}
	host.ID = strings.TrimSpace(host.ID)
	if strings.ContainsAny(host.ID, "/\\\x00") {
		return nil, errors.New("invalid local host ID")
	}
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		host.IsDefault = true
	}
	found := false
	for i := range hosts {
		if host.IsDefault {
			hosts[i].IsDefault = false
		}
		if hosts[i].ID == host.ID {
			hosts[i] = host
			found = true
		}
	}
	if !found {
		hosts = append(hosts, host)
	}
	if host.RemoveAPIKey {
		err = c.setLocalHostSecret(ctx, host.ID, host.IsDefault, "")
	} else if host.APIKey != "" {
		err = c.setLocalHostSecret(ctx, host.ID, host.IsDefault, host.APIKey)
	}
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		hosts[i].APIKey = ""
		hosts[i].RemoveAPIKey = false
	}
	raw, err := json.Marshal(hosts)
	if err == nil {
		err = c.SetSetting(ctx, ScopeCLI, localHostsSetting, raw)
	}
	if err != nil {
		return nil, err
	}
	for i := range hosts {
		if hosts[i].ID == host.ID {
			key, _ := c.localHostSecret(ctx, host.ID, host.IsDefault)
			hosts[i].HasAPIKey = key != ""
			return &hosts[i], nil
		}
	}
	return nil, errors.New("saved local host was not found")
}

func (c *Core) DeleteLocalHost(ctx context.Context, id string) error {
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return err
	}
	next := make([]LocalHost, 0, len(hosts))
	wasDefault := false
	for _, host := range hosts {
		if host.ID == id {
			wasDefault = host.IsDefault
			continue
		}
		next = append(next, host)
	}
	if len(next) == len(hosts) {
		return errors.New("local host not found")
	}
	if wasDefault && len(next) > 0 {
		next[0].IsDefault = true
	}
	if err := c.setLocalHostSecret(ctx, id, wasDefault, ""); err != nil {
		return err
	}
	raw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	return c.SetSetting(ctx, ScopeCLI, localHostsSetting, raw)
}

func (c *Core) TestLocalHost(ctx context.Context, id, endpoint, apiKey string) ([]string, error) {
	if apiKey == "" && id != "" {
		hosts, _ := c.ListLocalHosts(ctx)
		for _, host := range hosts {
			if host.ID == id {
				apiKey, _ = c.localHostSecret(ctx, id, host.IsDefault)
				break
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return ollama.ListModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
}
