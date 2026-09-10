package core

// Optional agent runtime manifests are authoring-time configuration. Existing
// agents have no runtime.json and resolve to the native CLI runtime exactly as
// before. Compatible single-agent manifests use the managed runtime; manifests
// that claim later sandbox or delegation phases fail closed.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const AgentRuntimeSchema = "praimate.runtime/v1"

type AgentRuntimeMode string

const (
	RuntimeNative  AgentRuntimeMode = "native"
	RuntimeAgentic AgentRuntimeMode = "agentic"
)

type AgentPreset string

const (
	PresetSimple      AgentPreset = "simple"
	PresetToolEnabled AgentPreset = "tool-enabled"
	PresetAutonomous  AgentPreset = "autonomous"
	PresetTeam        AgentPreset = "team"
	PresetCustom      AgentPreset = "custom"
)

type AgentCapabilities struct {
	ReadProject      bool `json:"read_project,omitempty"`
	AnalyzeCode      bool `json:"analyze_code,omitempty"`
	UseGit           bool `json:"use_git,omitempty"`
	ExecuteCommands  bool `json:"execute_commands,omitempty"`
	ModifyFiles      bool `json:"modify_files,omitempty"`
	Network          bool `json:"network,omitempty"`
	ExternalServices bool `json:"external_services,omitempty"`
}

type AgentRuntimeFeatures struct {
	ManagedTools  bool `json:"managed_tools,omitempty"`
	WorkingMemory bool `json:"working_memory,omitempty"`
	Sandbox       bool `json:"sandbox,omitempty"`
	Artifacts     bool `json:"artifacts,omitempty"`
	Checkpoints   bool `json:"checkpoints,omitempty"`
	Delegation    bool `json:"delegation,omitempty"`
	MaxChildren   int  `json:"max_children,omitempty"`
}

type AgentRuntimePermissions struct {
	DefaultTools string `json:"default_tools,omitempty"`
}

type AgentRuntimeLimits struct {
	MaxTotalInputBytes int64 `json:"max_total_input_bytes,omitempty"`
	MaxTurns           int   `json:"max_turns,omitempty"`
	MaxContextChars    int   `json:"max_context_chars,omitempty"`
	MaxOutputChars     int   `json:"max_output_chars,omitempty"`
}

type AgentRuntimeManifest struct {
	Schema       string                  `json:"schema"`
	PresetOrigin AgentPreset             `json:"preset_origin,omitempty"`
	Mode         AgentRuntimeMode        `json:"mode"`
	Capabilities AgentCapabilities       `json:"capabilities"`
	Features     AgentRuntimeFeatures    `json:"features"`
	Permissions  AgentRuntimePermissions `json:"permissions"`
	Limits       AgentRuntimeLimits      `json:"limits"`
}

type EffectiveAgentConfig struct {
	Mode                AgentRuntimeMode      `json:"mode"`
	PresetOrigin        AgentPreset           `json:"presetOrigin,omitempty"`
	DefaultTools        string                `json:"defaultTools,omitempty"`
	NativeCompatible    bool                  `json:"nativeCompatible"`
	AgenticCompatible   bool                  `json:"agenticCompatible"`
	RequiredFeatures    []string              `json:"requiredFeatures"`
	UnsupportedFeatures []string              `json:"unsupportedFeatures"`
	Manifest            *AgentRuntimeManifest `json:"manifest,omitempty"`
}

type GuidedAgentRequest struct {
	Name         string            `json:"name"`
	Purpose      string            `json:"purpose"`
	Knowledge    string            `json:"knowledge,omitempty"`
	Preset       AgentPreset       `json:"preset"`
	Supports     []string          `json:"supports,omitempty"`
	Capabilities AgentCapabilities `json:"capabilities"`
}

type GuidedAgentPreview struct {
	Agent             *Agent                `json:"agent"`
	Runtime           *AgentRuntimeManifest `json:"runtime"`
	AgentYAML         string                `json:"agentYaml"`
	RuntimeJSON       string                `json:"runtimeJson"`
	CapabilitySummary []string              `json:"capabilitySummary"`
	Warnings          []string              `json:"warnings"`
}

var guidedAgentIDRE = regexp.MustCompile(`[^a-z0-9-]+`)

func AgentRuntimePath(id string) (string, error) {
	dir, err := AgentDir(id)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime.json"), nil
}

func ParseAgentRuntime(raw []byte) (*AgentRuntimeManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var manifest AgentRuntimeManifest
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse runtime.json: %w", err)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, err
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return &manifest, nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("parse runtime.json: multiple JSON values")
		}
		return fmt.Errorf("parse runtime.json: %w", err)
	}
	return nil
}

func MarshalAgentRuntime(manifest *AgentRuntimeManifest) ([]byte, error) {
	if manifest == nil {
		return nil, errors.New("runtime manifest is required")
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func (m *AgentRuntimeManifest) Validate() error {
	if m.Schema != AgentRuntimeSchema {
		return fmt.Errorf("runtime schema %q is unsupported; expected %q", m.Schema, AgentRuntimeSchema)
	}
	switch m.Mode {
	case RuntimeNative, RuntimeAgentic:
	default:
		return fmt.Errorf("runtime mode %q is unsupported", m.Mode)
	}
	switch m.PresetOrigin {
	case "", PresetSimple, PresetToolEnabled, PresetAutonomous, PresetTeam, PresetCustom:
	default:
		return fmt.Errorf("runtime preset_origin %q is unsupported", m.PresetOrigin)
	}
	if !validToolLevel(m.Permissions.DefaultTools) {
		return fmt.Errorf("runtime default_tools %q is unsupported", m.Permissions.DefaultTools)
	}
	if m.Features.MaxChildren < 0 || m.Features.MaxChildren > 32 {
		return errors.New("runtime max_children must be between 0 and 32")
	}
	if m.Features.Delegation && m.Features.MaxChildren == 0 {
		return errors.New("runtime delegation requires max_children")
	}
	if !m.Features.Delegation && m.Features.MaxChildren != 0 {
		return errors.New("runtime max_children requires delegation")
	}
	if m.Mode == RuntimeNative && len(requiredAgenticFeatures(m.Features)) > 0 {
		return errors.New("native runtime cannot claim managed agentic features")
	}
	if m.Mode == RuntimeAgentic && len(requiredAgenticFeatures(m.Features)) == 0 {
		return errors.New("agentic runtime must request at least one managed feature")
	}
	if m.Limits.MaxTotalInputBytes < 0 {
		return errors.New("runtime max_total_input_bytes must not be negative")
	}
	if m.Limits.MaxTurns < 0 || m.Limits.MaxTurns > 100 {
		return errors.New("runtime max_turns must be between 1 and 100 when set")
	}
	if m.Limits.MaxContextChars < 0 || (m.Limits.MaxContextChars > 0 && m.Limits.MaxContextChars < 2000) {
		return errors.New("runtime max_context_chars must be at least 2000 when set")
	}
	if m.Limits.MaxOutputChars < 0 || (m.Limits.MaxOutputChars > 0 && m.Limits.MaxOutputChars < 500) {
		return errors.New("runtime max_output_chars must be at least 500 when set")
	}
	return nil
}

func LoadAgentRuntime(id string) (*AgentRuntimeManifest, error) {
	path, err := AgentRuntimePath(id)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParseAgentRuntime(raw)
}

func SaveAgentRuntime(id string, manifest *AgentRuntimeManifest) error {
	raw, err := MarshalAgentRuntime(manifest)
	if err != nil {
		return err
	}
	path, err := AgentRuntimePath(id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runtime-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return replaceRuntimeFile(tmpPath, path)
}

// replaceRuntimeFile uses the atomic replacement supported by Unix first,
// then falls back to a reversible two-rename swap for Windows, where Rename
// does not replace an existing destination.
func replaceRuntimeFile(staged, live string) error {
	if err := os.Rename(staged, live); err == nil {
		return nil
	}
	if _, err := os.Stat(live); err != nil {
		return fmt.Errorf("replace runtime.json: %w", err)
	}
	backupFile, err := os.CreateTemp(filepath.Dir(live), ".runtime-backup-*.json")
	if err != nil {
		return err
	}
	backup := backupFile.Name()
	if err := backupFile.Close(); err != nil {
		return err
	}
	if err := os.Remove(backup); err != nil {
		return err
	}
	defer os.Remove(backup)
	if err := os.Rename(live, backup); err != nil {
		return fmt.Errorf("backup runtime.json: %w", err)
	}
	if err := os.Rename(staged, live); err != nil {
		_ = os.Rename(backup, live)
		return fmt.Errorf("replace runtime.json: %w", err)
	}
	return nil
}

func (c *Core) ResolveEffectiveAgentConfig(_ context.Context, agent *Agent) (*EffectiveAgentConfig, error) {
	effective := &EffectiveAgentConfig{Mode: RuntimeNative, NativeCompatible: true, RequiredFeatures: []string{}, UnsupportedFeatures: []string{}}
	if agent == nil {
		return effective, nil
	}
	manifest, err := LoadAgentRuntime(agent.ID)
	if err != nil {
		return nil, fmt.Errorf("agent %q runtime: %w", agent.ID, err)
	}
	if manifest == nil {
		return effective, nil
	}
	effective.Mode = manifest.Mode
	effective.PresetOrigin = manifest.PresetOrigin
	effective.DefaultTools = manifest.Permissions.DefaultTools
	effective.RequiredFeatures = requiredAgenticFeatures(manifest.Features)
	effective.NativeCompatible = manifest.Mode == RuntimeNative
	effective.UnsupportedFeatures = unsupportedAgenticFeatures(manifest.Features)
	effective.AgenticCompatible = manifest.Mode == RuntimeAgentic && len(effective.UnsupportedFeatures) == 0
	effective.Manifest = manifest
	return effective, nil
}

func unsupportedAgenticFeatures(f AgentRuntimeFeatures) []string {
	var out []string
	if !f.ManagedTools {
		out = append(out, "managed tools")
	}
	if !f.WorkingMemory {
		out = append(out, "working memory")
	}
	if !f.Artifacts {
		out = append(out, "artifact store")
	}
	if f.Sandbox {
		out = append(out, "sandbox")
	}
	if f.Delegation {
		out = append(out, "delegation")
	}
	return out
}

func requiredAgenticFeatures(f AgentRuntimeFeatures) []string {
	var out []string
	if f.ManagedTools {
		out = append(out, "managed tools")
	}
	if f.WorkingMemory {
		out = append(out, "working memory")
	}
	if f.Sandbox {
		out = append(out, "sandbox")
	}
	if f.Artifacts {
		out = append(out, "artifact store")
	}
	if f.Checkpoints {
		out = append(out, "checkpoints")
	}
	if f.Delegation {
		out = append(out, "delegation")
	}
	return out
}

func PreviewGuidedAgent(req GuidedAgentRequest) (*GuidedAgentPreview, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.Purpose = strings.TrimSpace(req.Purpose)
	if req.Name == "" || req.Purpose == "" {
		return nil, errors.New("guided agent requires a name and purpose")
	}
	if req.Preset == "" {
		req.Preset = PresetSimple
	}
	if req.Preset == PresetCustom {
		return nil, errors.New("custom runtime configuration must be created in the manual editor")
	}
	if req.Preset == PresetTeam {
		return nil, errors.New("Team guided creation is unavailable until delegation execution is implemented")
	}
	id := strings.Trim(guidedAgentIDRE.ReplaceAllString(strings.ToLower(req.Name), "-"), "-")
	if id == "" {
		return nil, errors.New("agent name needs at least one letter or digit")
	}
	if len(req.Supports) == 0 {
		req.Supports = []string{"claude", "openclaude", "codex", "opencode", "praimate-code"}
	}
	agent := &Agent{
		ID: id, Name: req.Name, Description: req.Purpose,
		Instructions: "You are " + req.Name + ".\n\nYour purpose:\n" + req.Purpose,
		Supports:     req.Supports, Surfaces: append([]string(nil), AllSurfaces...), Knowledge: req.Knowledge,
	}
	manifest, summary, warnings, err := expandAgentPreset(req.Preset, req.Capabilities)
	if err != nil {
		return nil, err
	}
	if manifest.Mode == RuntimeAgentic {
		agent.Surfaces = []string{"chat", "editor"}
	}
	agent.Tools = capabilityToolNames(manifest.Capabilities)
	if err := agent.Validate(); err != nil {
		return nil, err
	}
	agentYAML, err := MarshalAgentYAML(agent)
	if err != nil {
		return nil, err
	}
	runtimeJSON, err := MarshalAgentRuntime(manifest)
	if err != nil {
		return nil, err
	}
	return &GuidedAgentPreview{
		Agent: agent, Runtime: manifest, AgentYAML: string(agentYAML), RuntimeJSON: string(runtimeJSON),
		CapabilitySummary: summary, Warnings: warnings,
	}, nil
}

func (c *Core) CreateGuidedAgent(ctx context.Context, req GuidedAgentRequest) (*Agent, error) {
	preview, err := PreviewGuidedAgent(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.GetAgent(ctx, preview.Agent.ID); err == nil {
		return nil, fmt.Errorf("an agent named %q (id %q) already exists", preview.Agent.Name, preview.Agent.ID)
	} else if !errors.Is(err, ErrAgentNotFound) {
		return nil, err
	}
	stored, err := c.upsertAgent(ctx, preview.Agent)
	if err != nil {
		return nil, err
	}
	if err := SaveAgentRuntime(stored.ID, preview.Runtime); err != nil {
		_ = c.DeleteAgent(ctx, stored.ID)
		if runtimePath, pathErr := AgentRuntimePath(stored.ID); pathErr == nil {
			_ = os.Remove(runtimePath)
		}
		return nil, fmt.Errorf("save guided runtime: %w", err)
	}
	return stored, nil
}

func expandAgentPreset(preset AgentPreset, requested AgentCapabilities) (*AgentRuntimeManifest, []string, []string, error) {
	m := &AgentRuntimeManifest{Schema: AgentRuntimeSchema, PresetOrigin: preset, Capabilities: requested}
	var warnings []string
	switch preset {
	case PresetSimple:
		m.Mode = RuntimeNative
	case PresetToolEnabled:
		m.Mode = RuntimeNative
		if !anyCapability(requested) {
			m.Capabilities.ReadProject = true
			m.Capabilities.AnalyzeCode = true
		}
		if m.Capabilities.ModifyFiles {
			m.Permissions.DefaultTools = "edits"
		} else if m.Capabilities.ExecuteCommands {
			m.Permissions.DefaultTools = "ask"
		}
	case PresetAutonomous:
		m.Mode = RuntimeAgentic
		m.Features = AgentRuntimeFeatures{ManagedTools: true, WorkingMemory: true, Artifacts: true, Checkpoints: true}
		m.Limits = AgentRuntimeLimits{MaxTurns: 12, MaxContextChars: 48000, MaxOutputChars: 8000}
		warnings = append(warnings, "Autonomous runs use capability-gated project, command, knowledge, and MCP tools. File changes, commands, mutating Git, and MCP calls require explicit approval; this preset does not claim OS-level sandbox isolation.")
	case PresetTeam:
		m.Mode = RuntimeAgentic
		m.Features = AgentRuntimeFeatures{ManagedTools: true, WorkingMemory: true, Sandbox: true, Artifacts: true, Checkpoints: true, Delegation: true, MaxChildren: 4}
		warnings = append(warnings, "Team execution is stored fail-closed until the coordinator and delegation runtime are installed by a later PrAImate phase.")
	default:
		return nil, nil, nil, fmt.Errorf("guided preset %q is unsupported", preset)
	}
	if err := m.Validate(); err != nil {
		return nil, nil, nil, err
	}
	summary := []string{"Runtime: " + string(m.Mode), "Preset: " + string(preset)}
	summary = append(summary, capabilityToolNames(m.Capabilities)...)
	summary = append(summary, requiredAgenticFeatures(m.Features)...)
	return m, summary, warnings, nil
}

func anyCapability(c AgentCapabilities) bool {
	return c.ReadProject || c.AnalyzeCode || c.UseGit || c.ExecuteCommands || c.ModifyFiles || c.Network || c.ExternalServices
}

func capabilityToolNames(c AgentCapabilities) []string {
	var out []string
	for _, capability := range []struct {
		enabled bool
		label   string
	}{
		{c.ReadProject, "read-project"}, {c.AnalyzeCode, "analyze-code"}, {c.UseGit, "git"},
		{c.ExecuteCommands, "execute-commands"}, {c.ModifyFiles, "modify-files"},
		{c.Network, "network"}, {c.ExternalServices, "external-services"},
	} {
		if capability.enabled {
			out = append(out, capability.label)
		}
	}
	return out
}
