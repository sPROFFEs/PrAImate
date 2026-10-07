package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/ollama"
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
	NativeModels  []string `json:"nativeModels,omitempty"`
}

// NativeModelAssignment is a model explicitly made available to praimate-cli.
// Credentials remain in the core vault and are resolved by endpoint at run time.
type NativeModelAssignment struct {
	HostID        string
	HostName      string
	Endpoint      string
	Model         string
	IsDefault     bool
	ContextTokens int
	OutputTokens  int
}

func (c *Core) NativeModelAssignments(ctx context.Context) ([]NativeModelAssignment, error) {
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return nil, err
	}
	var out []NativeModelAssignment
	seen := map[string]bool{}
	for _, host := range hosts {
		for _, model := range host.NativeModels {
			model = strings.TrimSpace(model)
			key := host.ID + "::" + model
			if model != "" && !seen[key] {
				seen[key] = true
				out = append(out, NativeModelAssignment{
					HostID: host.ID, HostName: host.Name, Endpoint: host.Endpoint,
					Model: model, IsDefault: host.IsDefault,
					ContextTokens: host.ContextTokens, OutputTokens: host.OutputTokens,
				})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDefault != out[j].IsDefault {
			return out[i].IsDefault
		}
		if out[i].HostName != out[j].HostName {
			return out[i].HostName < out[j].HostName
		}
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func localHostKey(id string) string { return "local_llm.host_key." + id }

func (c *Core) localHostSecret(ctx context.Context, id string, isDefault bool) (string, error) {
	key := localHostKey(id)
	if isDefault {
		key = "local_llm.api_key"
	}
	raw, err := c.GetSetting(ctx, ScopeCLI, key)
	if err == nil && len(raw) == 0 && isDefault {
		raw, err = c.GetSetting(ctx, ScopeCLI, localHostKey(id))
	}
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
	raw, err := json.Marshal(key)
	if err != nil {
		return err
	}
	settings := []string{localHostKey(id)}
	if isDefault {
		settings = append(settings, "local_llm.api_key")
	}
	for _, setting := range settings {
		if err := c.SetSetting(ctx, ScopeCLI, setting, raw); err != nil {
			return err
		}
	}
	return nil
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
	if strings.ContainsAny(host.ID, "/\\\x00") || strings.Contains(host.ID, "::") {
		return nil, errors.New("invalid local host ID")
	}
	hosts, err := c.ListLocalHosts(ctx)
	if err != nil {
		return nil, err
	}
	if len(hosts) == 0 {
		host.IsDefault = true
	}
	secrets := map[string]string{}
	for _, old := range hosts {
		key, err := c.localHostSecret(ctx, old.ID, old.IsDefault)
		if err != nil {
			return nil, err
		}
		secrets[old.ID] = key
		// Back up the legacy global-only key before changing the default.
		if err := c.setLocalHostSecret(ctx, old.ID, false, key); err != nil {
			return nil, err
		}
	}
	found := false
	for i := range hosts {
		if host.IsDefault {
			hosts[i].IsDefault = false
		}
		if hosts[i].ID == host.ID {
			if host.ActiveModels == nil {
				host.ActiveModels = hosts[i].ActiveModels
			}
			if host.NativeModels == nil {
				host.NativeModels = hosts[i].NativeModels
			}
			hosts[i] = host
			found = true
		}
	}
	if !found {
		hosts = append(hosts, host)
	}
	key := secrets[host.ID]
	if host.APIKey != "" {
		key = host.APIKey
	}
	if host.RemoveAPIKey {
		key = ""
	}
	err = c.setLocalHostSecret(ctx, host.ID, host.IsDefault, key)
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
	if wasDefault && len(next) > 0 {
		key, err := c.localHostSecret(ctx, next[0].ID, false)
		if err != nil {
			return err
		}
		if err := c.setLocalHostSecret(ctx, next[0].ID, true, key); err != nil {
			return err
		}
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
	ctx, err := c.LocalHostTLSContext(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return ollama.ListCanonicalModels(ctx, ollama.NormalizeEndpoint(endpoint), apiKey)
}
