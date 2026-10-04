package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/core"
)

const configKey = "orchestrated_workers_v1"

type Tier string

const (
	Primary Tier = "primary"
	Middle  Tier = "middle"
	Fast    Tier = "fast"
)

type Profile struct {
	Tier            Tier   `json:"tier"`
	Runtime         string `json:"runtime"` // "cli" or "native"
	CLI             string `json:"cli,omitempty"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"` // Codex only; empty uses CLI default
	Endpoint        string `json:"endpoint,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
	AllowEdits      bool   `json:"allowEdits,omitempty"`
	AllowCommands   bool   `json:"allowCommands,omitempty"`
	TimeoutSeconds  int    `json:"timeoutSeconds"`
	MaxInputBytes   int    `json:"maxInputBytes"`
	MaxOutputTokens int    `json:"maxOutputTokens"`
}

type Config struct {
	Workspace string    `json:"workspace"`
	Profiles  []Profile `json:"profiles"`
}

func (c Config) Profile(tier Tier) (Profile, bool) {
	for _, p := range c.Profiles {
		if p.Tier == tier {
			return p, true
		}
	}
	return Profile{}, false
}

func (c Config) Validate() error {
	if c.Workspace == "" || !filepath.IsAbs(c.Workspace) {
		return errors.New("worker workspace must be an absolute path")
	}
	if len(c.Profiles) != 3 {
		return errors.New("configure exactly one primary, middle and fast worker")
	}
	seen := map[Tier]bool{}
	for _, p := range c.Profiles {
		if p.Tier != Primary && p.Tier != Middle && p.Tier != Fast {
			return fmt.Errorf("unknown worker tier %q", p.Tier)
		}
		if seen[p.Tier] {
			return fmt.Errorf("duplicate worker tier %q", p.Tier)
		}
		seen[p.Tier] = true
		if strings.TrimSpace(p.Model) == "" {
			return fmt.Errorf("%s worker requires a model", p.Tier)
		}
		if p.ReasoningEffort != "" {
			if p.Runtime != "cli" || p.CLI != "codex" {
				return fmt.Errorf("%s worker reasoning effort is supported only by Codex", p.Tier)
			}
			switch p.ReasoningEffort {
			case "low", "medium", "high", "xhigh", "max", "ultra":
			default:
				return fmt.Errorf("%s worker has an unsupported reasoning effort", p.Tier)
			}
		}
		if len(p.Instructions) > 8<<10 {
			return fmt.Errorf("%s worker instructions exceed 8192 bytes", p.Tier)
		}
		if p.TimeoutSeconds < 0 || p.TimeoutSeconds > 3600 || p.MaxInputBytes < 1024 || p.MaxInputBytes > 1<<20 || p.MaxOutputTokens < 0 || p.MaxOutputTokens > 32768 {
			return fmt.Errorf("%s worker limits are outside supported bounds", p.Tier)
		}
		switch p.Runtime {
		case "cli":
			if p.CLI == "" || p.Endpoint != "" || p.MaxOutputTokens != 0 {
				return fmt.Errorf("%s CLI worker requires a CLI and no output token limit", p.Tier)
			}
		case "native":
			if p.CLI != "" || p.MaxOutputTokens == 0 {
				return fmt.Errorf("%s native worker requires an endpoint and output token limit", p.Tier)
			}
			u, err := url.Parse(p.Endpoint)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return fmt.Errorf("%s native worker endpoint must be an HTTP(S) base URL without credentials", p.Tier)
			}
		default:
			return fmt.Errorf("%s worker runtime must be cli or native", p.Tier)
		}
	}
	for _, p := range c.Profiles {
		if len(workerInstructions(c, p.Tier, p))+512 > p.MaxInputBytes {
			return fmt.Errorf("%s worker input limit is too small for its instructions", p.Tier)
		}
	}
	return nil
}

func (p Profile) Timeout() time.Duration { return time.Duration(p.TimeoutSeconds) * time.Second }

// LoadConfig and SaveConfig use the existing shared settings store. No schema
// migration or credential-bearing worker record is introduced by this phase.
func LoadConfig(ctx context.Context, c *core.Core) (Config, error) {
	raw, err := c.GetSetting(ctx, core.ScopeCLI, configKey)
	if err != nil || raw == nil {
		return Config{}, err
	}
	var config Config
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, err
	}
	return config, config.Validate()
}

func SaveConfig(ctx context.Context, c *core.Core, config Config) error {
	if err := config.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return c.SetSetting(ctx, core.ScopeCLI, configKey, raw)
}
