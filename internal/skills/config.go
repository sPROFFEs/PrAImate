package skills

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"gopkg.in/yaml.v3"
)

func ParseSkillConfigYAML(body []byte) (*SkillConfig, error) {
	if len(body) > maxRegistrySnapshot {
		return nil, errors.New("skill config size limit exceeded")
	}
	d := yaml.NewDecoder(bytes.NewReader(body))
	var c SkillConfig
	if err := d.Decode(&c); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("multiple skill config documents are unsupported")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// YAML authoring uses the identical JSON validator rather than a more lenient
// second DTO. Reject aliases/merges/unknown fields and missing false/zero values.
func (c *SkillConfig) UnmarshalYAML(node *yaml.Node) error {
	nodes := 0
	if err := validateManifestNode(node, 0, &nodes); err != nil {
		return err
	}
	stack := []*yaml.Node{node}
	size := 0
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		size += len(n.Value)
		if size > maxRegistrySnapshot {
			return errors.New("skill config size limit exceeded")
		}
		switch n.Tag {
		case "!!map", "!!seq", "!!str", "!!int", "!!bool", "!!null":
		default:
			return errors.New("unsupported skill config YAML tag")
		}
		stack = append(stack, n.Content...)
	}
	var value any
	if err := node.Decode(&value); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.UnmarshalJSON(body)
}

const SkillConfigSchema = "praimate.skills-config/v1"

// SkillConfig is preference data, never host authority. A nil pointer or
// Configured=false inherits; Configured=true with [] explicitly selects none.
// Required JSON fields deliberately match the portable proposed contract.
type SkillConfig struct {
	Schema      string         `json:"schema" yaml:"schema"`
	Configured  bool           `json:"configured" yaml:"configured"`
	Lockfile    string         `json:"lockfile" yaml:"lockfile"`
	Enforcement string         `json:"enforcement" yaml:"enforcement"`
	Bindings    []SkillBinding `json:"bindings" yaml:"bindings"`
	Budget      SkillBudget    `json:"budget" yaml:"budget"`
}
type SkillBinding struct {
	Ref        string `json:"ref" yaml:"ref"`
	Activation string `json:"activation" yaml:"activation"`
	Optional   bool   `json:"optional" yaml:"optional"`
}
type SkillBudget struct {
	CatalogTokens           int `json:"catalog_tokens" yaml:"catalog_tokens"`
	BodyTokens              int `json:"body_tokens" yaml:"body_tokens"`
	ResourceTokens          int `json:"resource_tokens" yaml:"resource_tokens"`
	TotalTokens             int `json:"total_tokens" yaml:"total_tokens"`
	MaxActive               int `json:"max_active" yaml:"max_active"`
	MaxLoadCallsPerTurn     int `json:"max_load_calls_per_turn" yaml:"max_load_calls_per_turn"`
	MaxResourceReadsPerTurn int `json:"max_resource_reads_per_turn" yaml:"max_resource_reads_per_turn"`
	MaxLoadedBytesFallback  int `json:"max_loaded_bytes_fallback" yaml:"max_loaded_bytes_fallback"`
}

func (b SkillBudget) Validate() error {
	if b.CatalogTokens < 0 || b.ResourceTokens < 0 || b.BodyTokens < 1 || b.TotalTokens < 1 || b.MaxActive < 1 || b.MaxLoadCallsPerTurn < 1 || b.MaxResourceReadsPerTurn < 1 || b.MaxLoadedBytesFallback < 1 {
		return errors.New("invalid skill budget")
	}
	return nil
}

// intersect never widens host restrictions. Zero catalogue/resource caps remain
// zero, not a signal to fall back to a preference. P4 enforces actual usage.
func (b SkillBudget) intersect(host SkillBudget) SkillBudget {
	return SkillBudget{CatalogTokens: min(b.CatalogTokens, host.CatalogTokens), BodyTokens: min(b.BodyTokens, host.BodyTokens), ResourceTokens: min(b.ResourceTokens, host.ResourceTokens), TotalTokens: min(b.TotalTokens, host.TotalTokens), MaxActive: min(b.MaxActive, host.MaxActive), MaxLoadCallsPerTurn: min(b.MaxLoadCallsPerTurn, host.MaxLoadCallsPerTurn), MaxResourceReadsPerTurn: min(b.MaxResourceReadsPerTurn, host.MaxResourceReadsPerTurn), MaxLoadedBytesFallback: min(b.MaxLoadedBytesFallback, host.MaxLoadedBytesFallback)}
}

func (c SkillConfig) Validate() error {
	if c.Schema != SkillConfigSchema {
		return errors.New("unsupported skill config schema")
	}
	if c.Enforcement != "controlled" && c.Enforcement != "compatible" {
		return errors.New("invalid skill enforcement")
	}
	// Label only; the resolver receives already-read lock bytes. It never follows
	// a package-provided path. Limit to a portable relative path for later packs.
	if err := validatePackagePath(c.Lockfile); err != nil {
		return errors.New("invalid skill lockfile path")
	}
	if len(c.Lockfile) > 255 || c.Bindings == nil || len(c.Bindings) > maxRegistryVersions {
		return errors.New("invalid skill bindings")
	}
	if !c.Configured && len(c.Bindings) != 0 {
		return errors.New("inherited skill config cannot contain bindings")
	}
	if err := c.Budget.Validate(); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, b := range c.Bindings {
		if !portableSkillRef.MatchString(b.Ref) || validatePackagePath(b.Ref) != nil || len(b.Ref) > 255 || seen[b.Ref] {
			return errors.New("invalid or duplicate skill ref")
		}
		seen[b.Ref] = true
		switch b.Activation {
		case "off", "manual", "auto", "pinned":
		default:
			return errors.New("invalid skill activation")
		}
	}
	return nil
}

// UnmarshalJSON rejects unknown/duplicate fields and missing required values,
// including optional=false and zero-valued catalogue/resource budgets.
func (c *SkillConfig) UnmarshalJSON(body []byte) error {
	if len(body) > maxRegistrySnapshot {
		return errors.New("skill config size limit exceeded")
	}
	if err := validateLockJSON(body); err != nil {
		return err
	}
	type plain SkillConfig
	var value plain
	if err := decodePackageRecord(body, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return err
	}
	required := func(m map[string]json.RawMessage, keys ...string) error {
		for _, key := range keys {
			raw := m[key]
			if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
				return errors.New("missing required skill config field")
			}
		}
		return nil
	}
	if err := required(fields, "schema", "configured", "lockfile", "enforcement", "bindings", "budget"); err != nil {
		return err
	}
	var budget map[string]json.RawMessage
	if err := json.Unmarshal(fields["budget"], &budget); err != nil {
		return err
	}
	if err := required(budget, "catalog_tokens", "body_tokens", "resource_tokens", "total_tokens", "max_active", "max_load_calls_per_turn", "max_resource_reads_per_turn", "max_loaded_bytes_fallback"); err != nil {
		return err
	}
	var bindings []map[string]json.RawMessage
	if err := json.Unmarshal(fields["bindings"], &bindings); err != nil {
		return err
	}
	for _, binding := range bindings {
		if err := required(binding, "ref", "activation", "optional"); err != nil {
			return err
		}
	}
	validated := SkillConfig(value)
	if err := validated.Validate(); err != nil {
		return err
	}
	*c = validated
	return nil
}

func cloneSkillConfig(c *SkillConfig) *SkillConfig {
	if c == nil {
		return nil
	}
	cloned := *c
	if c.Bindings != nil {
		cloned.Bindings = make([]SkillBinding, len(c.Bindings))
		copy(cloned.Bindings, c.Bindings)
	}
	return &cloned
}
