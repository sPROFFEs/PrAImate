package ollama

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Provider-specific names prevent one local host's key being used by another
// provider or subagent in the same OpenCode process. No key enters JSON files.
func OpenCodeAPIKeyEnv(provider string) string {
	hash := sha256.Sum256([]byte(provider))
	return "PRAIMATE_LLM_" + strings.ToUpper(fmt.Sprintf("%x", hash[:8]))
}

// ConfiguredOpenCodeRoute locates the selected provider's endpoint without
// returning its credentials. Project routes override global provider options.
func ConfiguredOpenCodeRoutes(cwd string) (map[string]Settings, string, error) {
	path, err := OpenCodeConfigPath()
	if err != nil {
		return nil, "", err
	}
	paths := []string{path}
	if override := os.Getenv("OPENCODE_CONFIG"); override != "" {
		paths = append(paths, override)
	}
	if cwd != "" {
		paths = append(paths, filepath.Join(cwd, "opencode.json"))
	}
	providers := map[string]map[string]any{}
	defaultModel := ""
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		var cfg struct {
			Model     string `json:"model"`
			Providers map[string]struct {
				Options map[string]any `json:"options"`
			} `json:"provider"`
		}
		// JSONC/custom CLI configs are left to the CLI's own resolver.
		if json.Unmarshal(raw, &cfg) != nil {
			continue
		}
		if cfg.Model != "" {
			defaultModel = cfg.Model
		}
		for name, provider := range cfg.Providers {
			if providers[name] == nil {
				providers[name] = map[string]any{}
			}
			for key, value := range provider.Options {
				providers[name][key] = value
			}
		}
	}
	routes := map[string]Settings{}
	for provider, options := range providers {
		endpoint, _ := options["baseURL"].(string)
		if endpoint != "" && !strings.Contains(endpoint, "{env:") {
			routes[provider] = Settings{Endpoint: NormalizeEndpoint(endpoint)}
		}
	}
	return routes, defaultModel, nil
}

func ConfiguredOpenCodeRoute(model, cwd string) (*Settings, error) {
	routes, defaultModel, err := ConfiguredOpenCodeRoutes(cwd)
	if err != nil {
		return nil, err
	}
	if model = strings.TrimSpace(model); model == "" {
		model = defaultModel
	}
	provider, id, qualified := strings.Cut(model, "/")
	if !qualified {
		return nil, nil
	}
	route, ok := routes[provider]
	if !ok {
		return nil, nil
	}
	route.Model = id
	return &route, nil
}
