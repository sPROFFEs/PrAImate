// Package ollama configures supported agent CLIs to talk to an
// OpenAI-compatible local endpoint.
package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Settings is what callers hand to local-route configuration. Stored as a JSON
// blob on the workspace (workspace.json's ollama field) so the choice
// follows the workspace, not the user's shell.
//
// APIKey is empty for vanilla Ollama (no auth) and non-empty for any
// OpenAI-compatible provider that requires Bearer auth (GPUStack,
// vLLM-with-key, LiteLLM gateways, etc.). When set, it's sent as
// `Authorization: Bearer <key>` on probe requests, injected into the
// agent's env as OPENAI_API_KEY at launch, and written into the
// per-agent config files where the provider expects it inline.
type Settings struct {
	Endpoint      string `json:"endpoint"`                // e.g. http://192.168.1.50:11434
	Model         string `json:"model"`                   // e.g. qwen3-coder
	WireAPI       string `json:"wireApi,omitempty"`       // codex: "chat" or "responses"
	APIKey        string `json:"apiKey,omitempty"`        // Bearer token; empty for Ollama
	ContextTokens int    `json:"contextTokens,omitempty"` // model context window hint
	OutputTokens  int    `json:"outputTokens,omitempty"`  // model max generation tokens
}

// NormalizeEndpoint trims, ensures the URL starts with http:// (we do
// not assume TLS — local Ollama deployments are almost always plaintext),
// and repairs common typos like "http:host:port" with no slashes —
// which would otherwise lead to "http://http:host:port" downstream when
// the value is used by Claude Code's URL parser.
func NormalizeEndpoint(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)

	// Already well-formed.
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return s
	}
	// Typo case: "http:host:port" or "https:host" — scheme present but
	// missing the //. Preserve the user's scheme while repairing it.
	scheme := "http://"
	if strings.HasPrefix(lower, "https:") {
		scheme = "https://"
		s = s[len("https:"):]
	} else if strings.HasPrefix(lower, "http:") {
		s = s[len("http:"):]
	}
	// Trim any stray slashes left over.
	s = strings.TrimLeft(s, "/")
	return scheme + s
}

// ListModels asks the endpoint what's loaded. Tries /api/tags first
// (Ollama-native), then /v1/models (OpenAI-compat). apiKey, if non-empty,
// is sent as `Authorization: Bearer <key>` — required by providers like
// GPUStack that gate /v1/models on auth. Vanilla Ollama ignores the
// header, so it's safe to always include when set.
func ListModels(ctx context.Context, endpoint, apiKey string) ([]string, error) {
	endpoint = NormalizeEndpoint(endpoint)
	if endpoint == "" {
		return nil, errors.New("empty endpoint")
	}
	// Saved OpenAI-compatible endpoints may already include /v1. Discovery
	// builds both the Ollama-native and OpenAI paths from the server root.
	endpoint = strings.TrimSuffix(endpoint, "/v1")
	cli := &http.Client{Timeout: 8 * time.Second}

	if models, err := tryOllamaTags(ctx, cli, endpoint, apiKey); err == nil && len(models) > 0 {
		return models, nil
	}
	return tryOpenAIModels(ctx, cli, endpoint, apiKey)
}

// addBearer attaches Authorization: Bearer <key> when key is non-empty.
// Centralised so every probe path stays consistent.
func addBearer(req *http.Request, apiKey string) {
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func tryOllamaTags(ctx context.Context, cli *http.Client, endpoint, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	addBearer(req, apiKey)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func tryOpenAIModels(ctx context.Context, cli *http.Client, endpoint, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	addBearer(req, apiKey)
	resp, err := cli.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

// OpenClaudeEnv returns the OpenAI-compatible environment expected by
// OpenClaude. Unlike Claude Code, OpenClaude does not speak the Anthropic
// protocol when routed to a local backend.
func OpenClaudeEnv(s Settings) map[string]string {
	if s.Endpoint == "" || s.Model == "" {
		return nil
	}
	env := OpenAIEnv(s)
	env["CLAUDE_CODE_USE_OPENAI"] = "1"
	env["OPENAI_MODEL"] = s.Model
	if raw := openClaudeLimitJSON(s.Model, env["OPENAI_BASE_URL"], s.ContextTokens); raw != "" {
		env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"] = raw
	}
	if raw := openClaudeLimitJSON(s.Model, env["OPENAI_BASE_URL"], s.OutputTokens); raw != "" {
		env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"] = raw
	}
	return env
}

func openClaudeLimitJSON(model, baseURL string, limit int) string {
	if strings.TrimSpace(model) == "" || limit <= 0 {
		return ""
	}
	entries := map[string]int{model: limit}
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		entries[parsed.Host+":"+model] = limit
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		return ""
	}
	return string(raw)
}

// OpenAIEnv returns the standard environment used by OpenAI-compatible CLI
// providers. The endpoint is normalized to the /v1 API root and the key falls
// back to a harmless non-empty token for local servers that ignore auth.
func OpenAIEnv(s Settings) map[string]string {
	if s.Endpoint == "" {
		return nil
	}
	token := s.APIKey
	if token == "" {
		token = "ollama"
	}
	base := NormalizeEndpoint(s.Endpoint)
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return map[string]string{
		"OPENAI_API_KEY":  token,
		"OPENAI_BASE_URL": base,
		"OPENAI_API_BASE": base,
	}
}

// CodexConfigPath is ~/.codex/config.toml.
func CodexConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// OpenCodeConfigPath is $XDG_CONFIG_HOME/opencode/opencode.json (or
// ~/.config/opencode/opencode.json on systems without XDG set).
func OpenCodeConfigPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "opencode", "opencode.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode", "opencode.json"), nil
}

const codexProviderName = "praimate_local"

// DisableCodex strips the praimate_local blocks from ~/.codex/config.toml.
// No-op if the file doesn't exist.
func DisableCodex() (string, error) {
	configPath, err := CodexConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return configPath, nil
	}
	if err != nil {
		return configPath, err
	}
	stripped := stripDeprecatedCodexRoute(string(raw))
	if stripped == string(raw) {
		return configPath, nil
	}
	return configPath, atomicWrite(configPath, []byte(stripped))
}

func stripDeprecatedCodexRoute(body string) string {
	body = stripCodexBlocks(body)
	lines := strings.Split(body, "\n")
	rootUsesManagedProvider := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			break
		}
		if trim == `model_provider = "`+codexProviderName+`"` {
			rootUsesManagedProvider = true
			break
		}
	}
	if !rootUsesManagedProvider {
		return body
	}
	out := make([]string, 0, len(lines))
	inRoot := true
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "[") {
			inRoot = false
		}
		if inRoot && (strings.HasPrefix(trim, "model_provider =") || strings.HasPrefix(trim, "model =")) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// stripCodexBlocks removes the [model_providers.praimate_local] and
// [profiles.praimate_local] tables from a TOML body. Stops when it hits
// the next [section] header or EOF.
func stripCodexBlocks(body string) string {
	headers := []string{
		"[model_providers." + codexProviderName + "]",
		"[profiles." + codexProviderName + "]",
	}
	out := body
	for _, h := range headers {
		out = stripTomlTable(out, h)
	}
	return out
}

func stripTomlTable(body, header string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if !skip && trim == header {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(trim, "[") {
			skip = false
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// opencodeConfig is what we serialize back to opencode.json. We keep
// unknown fields by round-tripping through map[string]any.
type opencodeConfig map[string]any

const openCodeProviderName = "praimate_local"

// OpenCodeUsesManagedLocalRoute reports whether an OpenCode-compatible
// launch will use PrAImate's praimate_local provider. An explicit model
// wins; otherwise OpenCode's configured default model decides.
func OpenCodeUsesManagedLocalRoute(model string) (bool, error) {
	if model = strings.TrimSpace(model); model != "" {
		return model == openCodeProviderName ||
			strings.HasPrefix(model, openCodeProviderName+"/"), nil
	}
	configPath, err := OpenCodeConfigPath()
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cfg := opencodeConfig{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, fmt.Errorf("parse OpenCode config: %w", err)
	}
	defaultModel, _ := cfg["model"].(string)
	return strings.HasPrefix(strings.TrimSpace(defaultModel), openCodeProviderName+"/"), nil
}

// ApplyOpenCode merges the Ollama provider into opencode.json. If the
// file exists, only the provider entry is overwritten — other config is
// preserved.
func ApplyOpenCode(s Settings, makeDefault bool) (string, error) {
	return ApplyOpenCodeModels(openCodeProviderName, "Ollama Remote", s, []string{s.Model}, makeDefault)
}

// ApplyOpenCodeModels merges multiple models for a specific provider into opencode.json.
func ApplyOpenCodeModels(providerKey, providerLabel string, s Settings, models []string, makeDefault bool) (string, error) {
	configPath, err := OpenCodeConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return configPath, err
	}

	cfg := opencodeConfig{}
	if raw, err := os.ReadFile(configPath); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}

	cfg["$schema"] = "https://opencode.ai/config.json"
	providers, _ := cfg["provider"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	if providerKey == "" {
		providerKey = openCodeProviderName
	}
	if providerLabel == "" {
		providerLabel = "Ollama Remote"
	}

	options := map[string]any{
		"baseURL": NormalizeEndpoint(s.Endpoint) + "/v1",
	}
	if s.APIKey != "" {
		// OpenCode expands {env:VAR} at runtime. Keep the credential out
		// of its plaintext config; PrAImate injects OPENAI_API_KEY when it
		// launches the configured local route.
		options["apiKey"] = "{env:OPENAI_API_KEY}"
	}

	var existingModels map[string]any
	if existingProv, ok := providers[providerKey].(map[string]any); ok {
		if em, ok := existingProv["models"].(map[string]any); ok {
			existingModels = em
		}
	}
	if existingModels == nil {
		existingModels = map[string]any{}
	}

	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		modelEntry := map[string]any{"name": m}
		if s.ContextTokens > 0 || s.OutputTokens > 0 {
			limit := map[string]any{}
			if s.ContextTokens > 0 {
				limit["context"] = s.ContextTokens
			}
			if s.OutputTokens > 0 {
				limit["output"] = s.OutputTokens
			}
			modelEntry["limit"] = limit
		}
		existingModels[m] = modelEntry
	}

	providers[providerKey] = map[string]any{
		"npm":     "@ai-sdk/openai-compatible",
		"name":    providerLabel,
		"options": options,
		"models":  existingModels,
	}
	cfg["provider"] = providers

	if makeDefault && len(models) > 0 && strings.TrimSpace(models[0]) != "" {
		cfg["model"] = providerKey + "/" + strings.TrimSpace(models[0])
		cfg["small_model"] = providerKey + "/" + strings.TrimSpace(models[0])
	}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return configPath, err
	}
	out = append(out, '\n')
	return configPath, atomicWrite(configPath, out)
}

// RemoveOpenCodeModel removes a single model from a provider in opencode.json.
func RemoveOpenCodeModel(providerKey, model string) (string, error) {
	configPath, err := OpenCodeConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return configPath, nil
	}
	if err != nil {
		return configPath, err
	}
	cfg := opencodeConfig{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return configPath, err
	}
	if providerKey == "" {
		providerKey = openCodeProviderName
	}
	providers, ok := cfg["provider"].(map[string]any)
	if !ok || providers == nil {
		return configPath, nil
	}
	prov, ok := providers[providerKey].(map[string]any)
	if !ok || prov == nil {
		return configPath, nil
	}
	models, ok := prov["models"].(map[string]any)
	if ok && models != nil {
		delete(models, model)
		if len(models) == 0 {
			delete(providers, providerKey)
		} else {
			prov["models"] = models
			providers[providerKey] = prov
		}
		cfg["provider"] = providers
	}
	targetModelRef := providerKey + "/" + model
	for _, k := range []string{"model", "small_model"} {
		if v, ok := cfg[k].(string); ok && v == targetModelRef {
			delete(cfg, k)
		}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return configPath, err
	}
	out = append(out, '\n')
	return configPath, atomicWrite(configPath, out)
}

// ListConfiguredOpenCodeModels returns the map of providerKey -> []modelNames from opencode.json.
func ListConfiguredOpenCodeModels() (map[string][]string, error) {
	configPath, err := OpenCodeConfigPath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := opencodeConfig{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	result := map[string][]string{}
	providers, _ := cfg["provider"].(map[string]any)
	for pKey, pVal := range providers {
		if prov, ok := pVal.(map[string]any); ok {
			if modelsMap, ok := prov["models"].(map[string]any); ok {
				var list []string
				for mName := range modelsMap {
					list = append(list, mName)
				}
				sort.Strings(list)
				result[pKey] = list
			}
		}
	}
	return result, nil
}

// OpenCodeConfigured reports whether opencode.json has our praimate_local
// provider entry.
func OpenCodeConfigured() bool {
	path, err := OpenCodeConfigPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	// Cheap check — full JSON parse not needed, the key string is
	// distinctive enough.
	return strings.Contains(string(raw), `"`+openCodeProviderName+`"`)
}

// DisableOpenCode removes the praimate_local provider entry. No-op if the
// file doesn't exist.
func DisableOpenCode() (string, error) {
	configPath, err := OpenCodeConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return configPath, nil
	}
	if err != nil {
		return configPath, err
	}
	cfg := opencodeConfig{}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return configPath, err
	}
	if providers, ok := cfg["provider"].(map[string]any); ok {
		delete(providers, openCodeProviderName)
		cfg["provider"] = providers
	}
	for _, k := range []string{"model", "small_model"} {
		if v, ok := cfg[k].(string); ok && strings.HasPrefix(v, openCodeProviderName+"/") {
			delete(cfg, k)
		}
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return configPath, err
	}
	out = append(out, '\n')
	return configPath, atomicWrite(configPath, out)
}

// DeepSeekConfigPath is ~/.deepseek/config.toml (the user-level file
// DeepSeek-TUI reads on startup).
func DeepSeekConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".deepseek", "config.toml"), nil
}

const (
	// Marker comments wrap our managed block in ~/.deepseek/config.toml
	// so re-applies can find and replace the block without touching
	// any hand-edited config the user wrote around it. The wrapper
	// approach matches what the bot scripts use for ~/.bashrc edits
	// and is more forgiving than the codex stripTomlTable approach
	// when the user has multiple [providers.*] blocks.
	deepseekBlockStart = "# >>> praimate ollama config"
	deepseekBlockEnd   = "# <<< praimate ollama config"
	// Legacy markers written by pre-rebrand builds — still stripped so
	// old config blocks don't duplicate when re-applying.
	deepseekLegacyStart = "# >>> clade ollama config"
	deepseekLegacyEnd   = "# <<< clade ollama config"
)

// ApplyDeepSeek writes the [providers.ollama] block + top-level
// provider/model defaults to ~/.deepseek/config.toml. The block is
// wrapped in marker comments so re-applies are clean and the user
// can keep their own config above and below the managed section.
//
// DeepSeek-TUI accepts the OpenAI-compat API on /v1, same as
// Codex/OpenCode, so the endpoint format matches.
func ApplyDeepSeek(s Settings) (string, error) {
	configPath, err := DeepSeekConfigPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return configPath, err
	}
	existing, _ := os.ReadFile(configPath)
	stripped := stripDeepSeekBlock(string(existing))

	// Keep credentials out of DeepSeek's plaintext config. PrAImate injects
	// OPENAI_API_KEY for launches using this route.
	keyLine := ""
	if s.APIKey != "" {
		keyLine = "api_key_env = \"OPENAI_API_KEY\"\n"
	}
	block := fmt.Sprintf(`
%s
provider = "ollama"
model = "%s"

[providers.ollama]
base_url = "%s/v1"
%s%s
`,
		deepseekBlockStart,
		s.Model,
		NormalizeEndpoint(s.Endpoint),
		keyLine,
		deepseekBlockEnd,
	)
	final := strings.TrimRight(stripped, "\n") + "\n" + block
	return configPath, atomicWrite(configPath, []byte(final))
}

// DisableDeepSeek strips the managed block from ~/.deepseek/config.toml.
// No-op when the file doesn't exist.
func DisableDeepSeek() (string, error) {
	configPath, err := DeepSeekConfigPath()
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return configPath, nil
	}
	if err != nil {
		return configPath, err
	}
	return configPath, atomicWrite(configPath, []byte(stripDeepSeekBlock(string(raw))))
}

// DeepSeekConfigured reports whether ~/.deepseek/config.toml has our
// managed block. Surfaces the pre-checked state on the Ollama screen
// when reopened.
func DeepSeekConfigured() bool {
	path, err := DeepSeekConfigPath()
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(raw), deepseekBlockStart) ||
		strings.Contains(string(raw), deepseekLegacyStart)
}

// stripDeepSeekBlock removes everything between our marker comments
// (inclusive). Lines outside the markers are preserved verbatim — no
// TOML parsing needed because we control the markers.
func stripDeepSeekBlock(body string) string {
	lines := strings.Split(body, "\n")
	var kept []string
	skip := false
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if trim == deepseekBlockStart || trim == deepseekLegacyStart {
			skip = true
			continue
		}
		if skip && (trim == deepseekBlockEnd || trim == deepseekLegacyEnd) {
			skip = false
			continue
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// CopyTo writes the given data to dst, creating parent dirs. Exposed for
// tests that want a sink other than os.WriteFile (e.g., capturing into a
// byte buffer).
func CopyTo(dst io.Writer, src io.Reader) (int64, error) { return io.Copy(dst, src) }
