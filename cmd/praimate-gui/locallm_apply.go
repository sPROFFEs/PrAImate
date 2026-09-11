package main

// Apply the saved local endpoint to OpenCode-compatible CLIs from the GUI.
// OpenClaude routes per launch; Claude Code remains on Anthropic.

import (
	"fmt"
	"strings"

	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
)

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
	if !targetHost.IsDefault {
		cleanID := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, targetHost.ID)
		provKey = "praimate_" + cleanID
	}

	switch cli {
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
		_ = a.SaveLocalHost(*targetHost)
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
		_ = a.SaveLocalHost(*targetHost)
		return fmt.Sprintf("Applied %s to OpenClaude profile (%s)", s.Model, targetHost.Name), nil

	default:
		return "", fmt.Errorf("apply-to-local supports opencode/praimate-code and openclaude — Claude Code stays on Anthropic")
	}
}

// RemoveModelFromCLI removes an individual model from a CLI's provider configuration.
func (a *App) RemoveModelFromCLI(cli, hostID, model string) (string, error) {
	hosts, _ := a.ListLocalHosts()
	var targetHost *LocalHost
	for i := range hosts {
		if hosts[i].ID == hostID || (hostID == "" && hosts[i].IsDefault) {
			targetHost = &hosts[i]
			break
		}
	}
	provKey := "praimate_local"
	if targetHost != nil && !targetHost.IsDefault {
		cleanID := strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, targetHost.ID)
		provKey = "praimate_" + cleanID
	}

	switch cli {
	case "opencode", "praimate-code":
		path, err := ollama.RemoveOpenCodeModel(provKey, model)
		if err != nil {
			return "", err
		}
		if targetHost != nil {
			targetHost.ActiveModels = removeString(targetHost.ActiveModels, model)
			_ = a.SaveLocalHost(*targetHost)
		}
		return fmt.Sprintf("Removed %s from %s — %s", model, cli, path), nil

	case "openclaude":
		if targetHost != nil {
			targetHost.ActiveModels = removeString(targetHost.ActiveModels, model)
			_ = a.SaveLocalHost(*targetHost)
		}
		return fmt.Sprintf("Removed %s from OpenClaude list", model), nil

	default:
		return "", fmt.Errorf("unsupported CLI %s", cli)
	}
}

// ListAppliedCLIModels returns all active models configured in OpenCode / OpenClaude.
func (a *App) ListAppliedCLIModels() ([]AppliedModelItem, error) {
	hosts, _ := a.ListLocalHosts()
	var out []AppliedModelItem
	ocModels, err := ollama.ListConfiguredOpenCodeModels()
	if err == nil {
		for pKey, models := range ocModels {
			var matchingHost *LocalHost
			for i := range hosts {
				h := &hosts[i]
				cleanID := strings.Map(func(r rune) rune {
					if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
						return r
					}
					return '_'
				}, h.ID)
				if (h.IsDefault && pKey == "praimate_local") || pKey == "praimate_"+cleanID {
					matchingHost = h
					break
				}
			}
			hostName := "Local LLM"
			endpoint := ""
			hostID := "default"
			if matchingHost != nil {
				hostName = matchingHost.Name
				endpoint = matchingHost.Endpoint
				hostID = matchingHost.ID
			}
			for _, m := range models {
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
	var next []string
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
