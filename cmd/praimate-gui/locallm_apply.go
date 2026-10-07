package main

// Apply the saved local endpoint to OpenCode-compatible CLIs from the GUI.
// OpenClaude routes per launch; Claude Code remains on Anthropic.

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/sPROFFEs/PrAImate/internal/launcher"
	"github.com/sPROFFEs/PrAImate/internal/ollama"
)

// Reuse the provider associated with the endpoint, even after default changes.
func localHostProviderKey(host *LocalHost) (string, error) {
	routes, _, err := ollama.ConfiguredOpenCodeRoutes("")
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(routes))
	for key := range routes {
		if strings.HasPrefix(key, "praimate_") {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if ollama.OpenAIEndpoint(routes[key].Endpoint) == ollama.OpenAIEndpoint(host.Endpoint) {
			return key, nil
		}
	}
	clean := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, host.ID)
	key := "praimate_" + clean
	if host.IsDefault {
		key = "praimate_local"
	}
	if existing, ok := routes[key]; ok && ollama.OpenAIEndpoint(existing.Endpoint) != ollama.OpenAIEndpoint(host.Endpoint) {
		hash := sha256.Sum256([]byte(host.ID + "\x00" + host.Endpoint))
		key = "praimate_" + clean + fmt.Sprintf("_%x", hash[:4])
	}
	return key, nil
}

// AppliedModelItem represents a model configured in a CLI.
type AppliedModelItem struct {
	HostID      string `json:"hostId"`
	HostName    string `json:"hostName"`
	Endpoint    string `json:"endpoint"`
	Model       string `json:"model"`
	ProviderKey string `json:"providerKey"`
	CLI         string `json:"cli"`
}

// ApplyModelsToCLI batch-applies models from a specific host into the CLI config.
func (a *App) ApplyModelsToCLI(cli, hostID string, models []string) (string, error) {
	if len(models) == 0 {
		return "", fmt.Errorf("select at least one model to apply")
	}
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return "", err
	}
	var targetHost *LocalHost
	for i := range hosts {
		if hosts[i].ID == hostID || (hostID == "" && hosts[i].IsDefault) {
			targetHost = &hosts[i]
			break
		}
	}
	if targetHost == nil {
		if hostID != "" {
			return "", fmt.Errorf("local host %q not found", hostID)
		}
		if len(hosts) > 0 {
			targetHost = &hosts[0]
		} else {
			return "", fmt.Errorf("no host configured")
		}
	}
	if strings.TrimSpace(targetHost.Endpoint) == "" {
		return "", fmt.Errorf("target host has no endpoint URL configured")
	}

	var apiKey string
	if targetHost.IsDefault {
		apiKey, _ = loadLocalLLMAPIKey(a.core)
	} else {
		apiKey, _ = loadHostAPIKey(a.core, targetHost.ID)
	}

	s := ollama.Settings{
		Endpoint:      targetHost.Endpoint,
		APIKey:        apiKey,
		Model:         strings.TrimSpace(models[0]),
		ContextTokens: targetHost.ContextTokens,
		OutputTokens:  targetHost.OutputTokens,
	}

	provKey := "praimate_local"
	if cli == "opencode" || cli == "praimate-code" {
		provKey, err = localHostProviderKey(targetHost)
		if err != nil {
			return "", err
		}
	}

	switch cli {
	case "praimate-cli":
		for _, m := range models {
			if m = strings.TrimSpace(m); m != "" && !containsString(targetHost.NativeModels, m) {
				targetHost.NativeModels = append(targetHost.NativeModels, m)
			}
		}
		if err := a.SaveLocalHost(*targetHost); err != nil {
			return "", err
		}
		return fmt.Sprintf("Assigned %d model(s) on %s to praimate-cli", len(models), targetHost.Name), nil

	case "opencode", "praimate-code":
		path, err := ollama.ApplyOpenCodeModels(provKey, targetHost.Name, s, models, targetHost.IsDefault)
		if err != nil {
			return "", err
		}
		// Save active models into host metadata
		for _, m := range models {
			if !containsString(targetHost.ActiveModels, m) {
				targetHost.ActiveModels = append(targetHost.ActiveModels, m)
			}
		}
		if err := a.SaveLocalHost(*targetHost); err != nil {
			return "", err
		}
		return fmt.Sprintf("Applied %d model(s) to %s (%s) — wrote %s", len(models), cli, targetHost.Name, path), nil

	case "openclaude":
		ls := launcher.OllamaSettings{
			Endpoint:      s.Endpoint,
			Model:         s.Model,
			APIKey:        s.APIKey,
			ContextTokens: s.ContextTokens,
			OutputTokens:  s.OutputTokens,
		}
		err := launcher.WriteOpenClaudeLocalProfile(ls, apiKey)
		if err != nil {
			return "", err
		}
		targetHost.ActiveModels = models
		if err := a.SaveLocalHost(*targetHost); err != nil {
			return "", err
		}
		return fmt.Sprintf("Applied %s to OpenClaude profile (%s)", s.Model, targetHost.Name), nil

	default:
		return "", fmt.Errorf("apply-to-local supports praimate-cli, opencode/praimate-code and openclaude — Claude Code stays on Anthropic")
	}
}

// RemoveModelFromCLI removes an individual model from a CLI's provider configuration.
func (a *App) RemoveModelFromCLI(cli, hostID, model string) (string, error) {
	return a.removeModelFromCLI(cli, hostID, "", model)
}

// RemoveAppliedModelFromCLI identifies the exact provider shown in the GUI.
// Several providers can expose the same model or use the same host endpoint.
func (a *App) RemoveAppliedModelFromCLI(cli, hostID, providerKey, model string) (string, error) {
	return a.removeModelFromCLI(cli, hostID, providerKey, model)
}

func (a *App) removeModelFromCLI(cli, hostID, providerKey, model string) (string, error) {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return "", err
	}
	var targetHost *LocalHost
	for i := range hosts {
		if hosts[i].ID == hostID || (hostID == "" && hosts[i].IsDefault) {
			targetHost = &hosts[i]
			break
		}
	}
	provKey := providerKey
	if cli == "opencode" || cli == "praimate-code" {
		if provKey == "" {
			if targetHost == nil {
				return "", fmt.Errorf("local host %q not found", hostID)
			}
			provKey, err = localHostProviderKey(targetHost)
			if err != nil {
				return "", err
			}
		} else {
			configured, err := ollama.ListConfiguredOpenCodeModels()
			if err != nil {
				return "", err
			}
			if _, ok := configured[provKey]; !ok {
				return "", fmt.Errorf("provider %q is no longer configured; refresh the model list", provKey)
			}
		}
	}

	switch cli {
	case "praimate-cli":
		if targetHost == nil {
			return "", fmt.Errorf("local host %q not found", hostID)
		}
		targetHost.NativeModels = append([]string{}, removeString(targetHost.NativeModels, model)...)
		if err := a.SaveLocalHost(*targetHost); err != nil {
			return "", err
		}
		return fmt.Sprintf("Removed %s from praimate-cli (%s)", model, targetHost.Name), nil

	case "opencode", "praimate-code":
		path, err := ollama.RemoveOpenCodeModel(provKey, model)
		if err != nil {
			return "", err
		}
		if targetHost != nil {
			targetHost.ActiveModels = removeString(targetHost.ActiveModels, model)
			if err := a.SaveLocalHost(*targetHost); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Removed %s from %s — %s", model, cli, path), nil

	case "openclaude":
		if err := launcher.BackupOpenClaudeLocalProfileIfPresent(); err != nil {
			return "", err
		}
		if targetHost != nil {
			targetHost.ActiveModels = removeString(targetHost.ActiveModels, model)
			if err := a.SaveLocalHost(*targetHost); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Removed %s from OpenClaude local routing", model), nil

	default:
		return "", fmt.Errorf("unsupported CLI %s", cli)
	}
}

// ListAppliedCLIModels returns all active models configured in OpenCode / OpenClaude.
func (a *App) ListAppliedCLIModels() ([]AppliedModelItem, error) {
	hosts, err := a.ListLocalHosts()
	if err != nil {
		return nil, err
	}
	var out []AppliedModelItem
	seen := make(map[string]bool)
	for _, host := range hosts {
		for _, model := range host.NativeModels {
			model = strings.TrimSpace(model)
			key := "praimate-cli:" + host.ID + ":" + model
			if model != "" && !seen[key] {
				seen[key] = true
				out = append(out, AppliedModelItem{
					HostID: host.ID, HostName: host.Name, Endpoint: host.Endpoint,
					Model: model, CLI: "praimate-cli",
				})
			}
		}
	}

	// 1. OpenCode / PrAImate Code models from opencode.json
	ocModels, err := ollama.ListConfiguredOpenCodeModels()
	if err == nil {
		for pKey, models := range ocModels {
			var matchingHost *LocalHost
			route, routeErr := ollama.ConfiguredOpenCodeRoute(pKey+"/model", "")
			for i := range hosts {
				if routeErr == nil && route != nil && ollama.OpenAIEndpoint(route.Endpoint) == ollama.OpenAIEndpoint(hosts[i].Endpoint) {
					matchingHost = &hosts[i]
					break
				}
			}
			hostName := pKey
			endpoint := ""
			hostID := ""
			if route != nil {
				endpoint = route.Endpoint
			}
			if matchingHost != nil {
				hostName = matchingHost.Name
				endpoint = matchingHost.Endpoint
				hostID = matchingHost.ID
			}
			for _, m := range models {
				key := "opencode:" + pKey + ":" + m
				if !seen[key] {
					seen[key] = true
					out = append(out, AppliedModelItem{
						HostID:      hostID,
						HostName:    hostName,
						Endpoint:    endpoint,
						Model:       m,
						ProviderKey: pKey,
						CLI:         "opencode / praimate-code",
					})
				}
			}
		}
	}

	// 2. OpenClaude local profile
	if profile, ok := launcher.ReadOpenClaudeLocalProfile(); ok && profile.Model != "" {
		var matchingHost *LocalHost
		for i := range hosts {
			h := &hosts[i]
			if ollama.OpenAIEndpoint(h.Endpoint) == ollama.OpenAIEndpoint(profile.BaseURL) {
				matchingHost = h
				break
			}
		}
		hostName := "Local LLM"
		endpoint := profile.BaseURL
		hostID := "default"
		if matchingHost != nil {
			hostName = matchingHost.Name
			endpoint = matchingHost.Endpoint
			hostID = matchingHost.ID
		}
		key := "openclaude:" + hostID + ":" + profile.Model
		if !seen[key] {
			seen[key] = true
			out = append(out, AppliedModelItem{
				HostID:      hostID,
				HostName:    hostName,
				Endpoint:    endpoint,
				Model:       profile.Model,
				ProviderKey: "openclaude",
				CLI:         "openclaude",
			})
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CLI != out[j].CLI {
			return out[i].CLI < out[j].CLI
		}
		if out[i].HostID != out[j].HostID {
			return out[i].HostID < out[j].HostID
		}
		if out[i].ProviderKey != out[j].ProviderKey {
			return out[i].ProviderKey < out[j].ProviderKey
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func containsString(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) []string {
	next := []string{}
	for _, v := range slice {
		if v != s {
			next = append(next, v)
		}
	}
	return next
}

// ollama_remote route applied. praimate-code is
// the OpenCode fork (name-only rebrand) and reads the SAME opencode.json,
// so the opencode route covers it.
type LocalCLIStatus struct {
	Opencode   bool `json:"opencode"` // also governs praimate-code (shared config)
	Openclaude bool `json:"openclaude"`
}

// LocalCLIStatusNow probes the on-disk config of the config-file CLIs.
func (a *App) LocalCLIStatusNow() LocalCLIStatus {
	return LocalCLIStatus{
		Opencode:   ollama.OpenCodeConfigured(),
		Openclaude: launcher.IsOpenClaudeConfigured(),
	}
}

// ApplyLocalToCLI writes the saved local endpoint into opencode.json so
// OpenCode and PrAImate Code route to the local model. model is
// required (it becomes the provider's default model). Returns a status
// line for the UI.
func (a *App) ApplyLocalToCLI(cli, model string) (string, error) {
	d, err := a.GetLocalLLM()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(d.Endpoint) == "" {
		return "", fmt.Errorf("no local endpoint saved — set and save one above first")
	}
	if strings.TrimSpace(model) == "" {
		return "", fmt.Errorf("pick a model to route %s to", cli)
	}
	apiKey, err := loadLocalLLMAPIKey(a.core)
	if err != nil {
		return "", fmt.Errorf("load local LLM credential: %w", err)
	}
	s := ollama.Settings{
		Endpoint:      d.Endpoint,
		APIKey:        apiKey,
		Model:         strings.TrimSpace(model),
		ContextTokens: d.ContextTokens,
		OutputTokens:  d.OutputTokens,
	}
	switch cli {
	case "opencode", "praimate-code":
		// Shared config: praimate-code is OpenCode rebranded name-only and
		// reads the same opencode.json, so one write routes both.
		path, err := ollama.ApplyOpenCode(s, true)
		if err != nil {
			return "", err
		}
		return "opencode + praimate-code routed to the local model — wrote " + path, nil
	case "openclaude":
		ls := launcher.OllamaSettings{
			Endpoint:      s.Endpoint,
			Model:         s.Model,
			APIKey:        s.APIKey,
			ContextTokens: s.ContextTokens,
			OutputTokens:  s.OutputTokens,
		}
		err := launcher.WriteOpenClaudeLocalProfile(ls, apiKey)
		if err != nil {
			return "", err
		}
		return "openclaude routed to the local model", nil
	default:
		return "", fmt.Errorf("apply-to-local supports opencode/praimate-code and openclaude — Claude Code stays on Anthropic")
	}
}

// DisableLocalForCLI removes the local ollama_remote route from a CLI's
// global config, returning it to its cloud default.
func (a *App) DisableLocalForCLI(cli string) (string, error) {
	switch cli {
	case "opencode", "praimate-code":
		path, err := ollama.DisableOpenCode()
		if err != nil {
			return "", err
		}
		return "opencode + praimate-code local route removed — " + path, nil
	case "openclaude":
		err := launcher.BackupOpenClaudeLocalProfileIfPresent()
		if err != nil {
			return "", err
		}
		return "openclaude local route removed", nil
	default:
		return "", fmt.Errorf("nothing to disable for %s", cli)
	}
}
