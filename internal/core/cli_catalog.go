package core

import (
	"context"
	"encoding/json"
	"git.jtsec.local/lab/PrAImate/internal/launcher"
	"git.jtsec.local/lab/PrAImate/internal/ollama"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CLIInfo describes one launchable CLI for the "new chat" picker.
type CLIInfo struct {
	Capabilities      CLICapabilities `json:"capabilities"`
	ID                string          `json:"id"`
	Label             string          `json:"label"`
	Available         bool            `json:"available"`
	UnavailableReason string          `json:"unavailableReason,omitempty"`
	ModelHint         string          `json:"modelHint"` // expected --model format, "" = no model flag
	Models            []string        `json:"models"`    // suggestions; free text always allowed
}

// modelHints documents each CLI's --model format. Empty string means
// the CLI has no model flag (the model input is disabled in the UI).
var modelHints = map[string]string{
	"claude":        "alias (sonnet, opus, haiku) or full model id",
	"openclaude":    "alias (sonnet, opus, haiku) or full model id",
	"codex":         "model id, e.g. gpt-5.1-codex",
	"opencode":      "provider/model, e.g. anthropic/claude-sonnet-4-5",
	"praimate-code": "provider/model, e.g. anthropic/claude-sonnet-4-5",
}

// staticModelSuggestions are fallback datalist entries for CLIs without
// a list command. They are SUGGESTIONS — the input stays free text, so
// new models work without a PrAImate release.
var staticModelSuggestions = map[string][]string{
	"claude":     {"sonnet", "opus", "haiku", "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"},
	"openclaude": {"sonnet", "opus", "haiku", "claude-opus-4-8", "claude-sonnet-4-6", "claude-haiku-4-5"},
	"codex":      {"gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5.1", "o4-mini"},
}

// ListCLIs returns every launchable CLI with availability probed
// concurrently (a --version run each, bounded at 5s total).
func ListCLIs(parent context.Context) []CLIInfo {
	agents := launcher.KnownAgents()
	out := make([]CLIInfo, len(agents))
	// 5s was too tight: a slow `opencode --version` (Bun cold start)
	// or a stalled `codex --version` would race past the deadline and
	// the CLI got rendered as "not installed" in the chat/agent
	// selector even though the CLIs tab (30s budget) showed it
	// installed. 20s leaves headroom for the slowest healthy probe.
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for i, ag := range agents {
		out[i] = CLIInfo{
			Capabilities: CapabilitiesForCLI(string(ag.ID)),
			ID:           string(ag.ID),
			Label:        ag.Label,
			ModelHint:    modelHints[string(ag.ID)],
			Models:       staticModelSuggestions[string(ag.ID)],
		}
		adapter, err := GetCLIAdapter(string(ag.ID))
		if err != nil {
			out[i].UnavailableReason = err.Error()
			continue
		}
		wg.Add(1)
		go func(i int, ad CLIAdapter) {
			defer wg.Done()
			if err := ad.Available(ctx); err != nil {
				out[i].UnavailableReason = err.Error()
			} else {
				out[i].Available = true
			}
		}(i, adapter)
	}
	wg.Wait()
	return out
}

// ListCLIModels returns live model suggestions when the CLI exposes a
// catalogue; otherwise it falls back to the static suggestions.
func ListCLIModels(parent context.Context, cli string) []string {
	seen := map[string]bool{}
	var models []string
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m != "" && !seen[m] {
			seen[m] = true
			models = append(models, m)
		}
	}

	if cli == "openclaude" {
		// Include OpenClaude configured local profile model if present
		if profile, ok := launcher.ReadOpenClaudeLocalProfile(); ok && profile.Model != "" {
			add(profile.Model)
		}
		// Include static aliases and models
		for _, m := range staticModelSuggestions["openclaude"] {
			add(m)
		}
		return models
	}

	if cli == "codex" {
		live := listCodexModels(parent)
		for _, m := range live {
			add(m)
		}
		for _, m := range staticModelSuggestions["codex"] {
			add(m)
		}
		return models
	}

	if cli == "opencode" || cli == "praimate-code" {
		// Include models configured in opencode.json
		if ocModels, err := ollama.ListConfiguredOpenCodeModels(); err == nil {
			for pKey, mList := range ocModels {
				for _, m := range mList {
					add(pKey + "/" + m)
					add(m)
				}
			}
		}
		// Probe opencode CLI models if available
		if bin, err := exec.LookPath(cli); err == nil {
			ctx, cancel := context.WithTimeout(parent, 8*time.Second)
			cmd := exec.CommandContext(ctx, bin, "models")
			hideConsole(cmd)
			if outBytes, err := cmd.Output(); err == nil {
				for _, line := range strings.Split(string(outBytes), "\n") {
					line = strings.TrimSpace(line)
					if line != "" && strings.Contains(line, "/") && !strings.Contains(line, " ") {
						add(line)
					}
				}
			}
			cancel()
		}
		// Fallback defaults
		for _, m := range []string{"anthropic/claude-sonnet-4-5", "openai/gpt-5", "anthropic/claude-opus-4-6", "anthropic/claude-haiku-4-5"} {
			add(m)
		}
		return models
	}

	for _, m := range staticModelSuggestions[cli] {
		add(m)
	}
	return models
}

func listCodexModels(parent context.Context) []string {
	bin, err := exec.LookPath("codex")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "debug", "models")
	hideConsole(cmd)
	outBytes, err := cmd.Output()
	if err != nil {
		return nil
	}
	return ParseCodexDebugModels(outBytes)
}

func ParseCodexDebugModels(raw []byte) []string {
	var payload struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	seen := map[string]bool{}
	var models []string
	for _, item := range payload.Models {
		slug := strings.TrimSpace(item.Slug)
		if slug == "" || seen[slug] || item.Visibility != "list" {
			continue
		}
		seen[slug] = true
		models = append(models, slug)
	}
	return models
}
