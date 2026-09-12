package launcher

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const openClaudeProfileFileName = ".openclaude-profile.json"

func openClaudeLocalLLMEnabled(settings WorkspaceSettings) bool {
	o := settings.Ollama
	return o.Endpoint != "" && o.Model != "" && o.HasAgent(AgentOpenClaude)
}

func openClaudeHomeForChat(c Chat) string {
	return homeDir()
}

func openClaudeHomeForWorkspace(ws Workspace) string {
	return homeDir()
}

type openClaudeProfile struct {
	Profile   string            `json:"profile"`
	Env       map[string]string `json:"env"`
	CreatedAt string            `json:"createdAt"`
}

func openClaudeProfilePath(home string) string {
	return filepath.Join(home, ".openclaude", openClaudeProfileFileName)
}

func openClaudeProfileBackupPath(home string) string {
	return openClaudeProfilePath(home) + ".bak"
}

func WriteOpenClaudeLocalProfile(settings OllamaSettings, authToken string) error {
	home := homeDir()
	if home == "" {
		return fmt.Errorf("no home dir resolved")
	}
	path := openClaudeProfilePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	env := map[string]string{
		"OPENAI_BASE_URL": openAICompatibleBaseURL(settings.Endpoint),
		"OPENAI_MODEL":    settings.Model,
		"OPENAI_API_KEY":  authToken,
	}
	if raw := openClaudeLimitJSON(settings.Model, openAICompatibleBaseURL(settings.Endpoint), settings.ContextTokens); raw != "" {
		env["CLAUDE_CODE_OPENAI_CONTEXT_WINDOWS"] = raw
	}
	if raw := openClaudeLimitJSON(settings.Model, openAICompatibleBaseURL(settings.Endpoint), settings.OutputTokens); raw != "" {
		env["CLAUDE_CODE_OPENAI_MAX_OUTPUT_TOKENS"] = raw
	}
	profile := openClaudeProfile{
		Profile:   "openai",
		Env:       env,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	raw, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := writeFileAtomic(path, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func BackupOpenClaudeLocalProfileIfPresent() error {
	home := homeDir()
	if home == "" {
		return nil
	}
	path := openClaudeProfilePath(home)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	bak := openClaudeProfileBackupPath(home)
	if err := os.Remove(bak); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale %s: %w", bak, err)
	}
	if err := os.Rename(path, bak); err != nil {
		return fmt.Errorf("rename %s to %s: %w", path, bak, err)
	}
	return nil
}

type OpenClaudeProfileInfo struct {
	Model   string
	BaseURL string
}

func ReadOpenClaudeLocalProfile() (OpenClaudeProfileInfo, bool) {
	home := homeDir()
	if home == "" {
		return OpenClaudeProfileInfo{}, false
	}
	path := openClaudeProfilePath(home)
	raw, err := os.ReadFile(path)
	if err != nil {
		return OpenClaudeProfileInfo{}, false
	}
	var profile openClaudeProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return OpenClaudeProfileInfo{}, false
	}
	if profile.Profile == "openai" && profile.Env["OPENAI_BASE_URL"] != "" {
		return OpenClaudeProfileInfo{
			Model:   profile.Env["OPENAI_MODEL"],
			BaseURL: profile.Env["OPENAI_BASE_URL"],
		}, true
	}
	return OpenClaudeProfileInfo{}, false
}

// IsOpenClaudeConfigured reports whether the OpenClaude local profile
// currently points to a local endpoint via the "openai" profile.
func IsOpenClaudeConfigured() bool {
	_, ok := ReadOpenClaudeLocalProfile()
	return ok
}
