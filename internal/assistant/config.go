// Package assistant implements the optional application operator. Providers
// generate structured intentions; only registered application actions execute.
package assistant

import (
	"errors"
	"fmt"
	"strings"
)

type Policy string

const (
	Allow Policy = "allow"
	Ask   Policy = "ask"
	Deny  Policy = "deny"
)

var Capabilities = []string{"read", "navigate", "chats", "workers", "agents", "skills", "mcp", "settings", "tasks", "delegate", "system", "network", "filesystem"}

type VoiceConfig struct {
	Enabled    bool   `json:"enabled"`
	ModelID    string `json:"model_id"`
	Language   string `json:"language"`
	Threads    int    `json:"threads"`
	AutoSend   bool   `json:"auto_send"`
	KeepLoaded bool   `json:"keep_loaded"`
	Shortcut   string `json:"shortcut"`
}

type Config struct {
	Enabled        bool              `json:"enabled"`
	ModelID        string            `json:"model_id"` // lfm-efficient, qwen-quality, existing
	Endpoint       string            `json:"endpoint,omitempty"`
	Model          string            `json:"model,omitempty"`
	Shortcut       string            `json:"shortcut"`
	StartWithApp   bool              `json:"start_with_app"`
	KeepLoaded     bool              `json:"keep_loaded"`
	IdleSeconds    int               `json:"idle_seconds"`
	Context        int               `json:"context"`
	Output         int               `json:"output"`
	Threads        int               `json:"threads"`
	GPULayers      int               `json:"gpu_layers"`
	Batch          int               `json:"batch"`
	Temperature    float64           `json:"temperature"`
	TopP           float64           `json:"top_p"`
	TopK           int               `json:"top_k"`
	RepeatPenalty  float64           `json:"repeat_penalty"`
	MaxTurns       int               `json:"max_turns"`
	MaxActions     int               `json:"max_actions"`
	MaxFailures    int               `json:"max_failures"`
	MaxDelegations int               `json:"max_delegations"`
	CommandSeconds int               `json:"command_seconds"`
	Permissions    map[string]Policy `json:"permissions"`
	Voice          VoiceConfig       `json:"voice"`
}

func DefaultConfig() Config {
	return Config{ModelID: "lfm-efficient", Shortcut: "Mod+Shift+Space", IdleSeconds: 120, Context: 2048, Output: 512, Threads: 2, Batch: 128, Temperature: .2, TopP: .9, TopK: 40, RepeatPenalty: 1.1, MaxTurns: 12, MaxActions: 10, MaxFailures: 3, MaxDelegations: 2, CommandSeconds: 60, Permissions: Preset("standard"), Voice: VoiceConfig{ModelID: "whisper-base", Language: "auto", Threads: 2, Shortcut: "Mod+Shift+M"}}
}
func Preset(name string) map[string]Policy {
	p := map[string]Policy{}
	for _, cap := range Capabilities {
		p[cap] = Deny
	}
	p["read"], p["navigate"] = Allow, Allow
	if name == "standard" || name == "full" {
		for _, cap := range []string{"chats", "workers", "agents", "skills", "mcp", "settings", "tasks", "delegate"} {
			p[cap] = Ask
			if name == "full" {
				p[cap] = Allow
			}
		}
	}
	return p
}
func (c Config) Validate() error {
	if c.ModelID != "lfm-efficient" && c.ModelID != "qwen-quality" && c.ModelID != "existing" {
		return errors.New("unsupported Assistant model")
	}
	if c.Enabled && c.ModelID == "existing" && (c.Endpoint == "" || c.Model == "") {
		return errors.New("select an existing local endpoint and model")
	}
	if c.Context < 2048 || c.Context > 131072 || c.Output < 64 || c.Output >= c.Context || c.Threads < 1 || c.Threads > 256 || c.GPULayers < 0 || c.GPULayers > 1024 || c.Batch < 1 || c.Batch > 8192 {
		return errors.New("invalid Assistant runtime limits")
	}
	if c.Temperature < 0 || c.Temperature > 2 || c.TopP <= 0 || c.TopP > 1 || c.TopK < 0 || c.TopK > 1000 || c.RepeatPenalty < .1 || c.RepeatPenalty > 3 || c.IdleSeconds < 10 || c.IdleSeconds > 86400 {
		return errors.New("invalid Assistant sampling/idle settings")
	}
	if c.MaxTurns < 1 || c.MaxTurns > 64 || c.MaxActions < 1 || c.MaxActions > 64 || c.MaxFailures < 1 || c.MaxFailures > 10 || c.MaxDelegations < 0 || c.MaxDelegations > 10 || c.CommandSeconds < 1 || c.CommandSeconds > 300 {
		return errors.New("invalid Assistant execution limits")
	}
	for _, cap := range Capabilities {
		p := c.Permissions[cap]
		if p != Allow && p != Ask && p != Deny {
			return fmt.Errorf("invalid permission for %s", cap)
		}
	}
	if c.Voice.ModelID != "whisper-tiny" && c.Voice.ModelID != "whisper-base" && c.Voice.ModelID != "whisper-small" {
		return errors.New("unsupported Voice model")
	}
	language := c.Voice.Language
	validLanguage := language == "auto" || ((len(language) == 2 || len(language) == 3) && strings.Trim(language, "abcdefghijklmnopqrstuvwxyz") == "")
	if !validLanguage {
		return errors.New("Voice language must be auto or a lowercase language code")
	}
	if c.Voice.Threads < 1 || c.Voice.Threads > 256 || len(c.Voice.Language) > 12 {
		return errors.New("invalid Voice settings")
	}
	if !ValidShortcut(c.Shortcut) || !ValidShortcut(c.Voice.Shortcut) || c.Shortcut == c.Voice.Shortcut {
		return errors.New("shortcuts must be distinct Mod+Shift+Space or Mod+Shift+letter combinations")
	}
	return nil
}
func ValidShortcut(s string) bool {
	return s == "Mod+Shift+Space" || (len(s) == 11 && s[:10] == "Mod+Shift+" && s[10] >= 'A' && s[10] <= 'Z')
}
