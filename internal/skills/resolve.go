package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sort"
)

// SkillScope contains bytes already acquired by the host. A lockfile label in
// Config does not authorize a filesystem read or network request.
type SkillScope struct {
	Config *SkillConfig
	Lock   []byte
}
type SkillScopes struct {
	Application, Project, Agent, Workflow, Session SkillScope
	ProjectTrusted                                 bool
}

// SkillResolutionPolicy is supplied by host code, not imported configuration.
// Transport describes a verified adapter capability, not a requested CLI mode.
// This resolver does not measure/enforce token use or private native context.
type SkillResolutionPolicy struct {
	Budget            SkillBudget
	Transport         string // controlled, compatible, unsupported (empty fails closed)
	RequireControlled bool
	DeniedSources     map[string]bool
	IncompatibleRefs  map[string]bool
}
type SkillDiagnostic struct {
	Code    string `json:"code,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Message string `json:"message,omitempty"`
	Remedy  string `json:"remedy,omitempty"`
}
type SkillResolutionError struct {
	Diagnostics []SkillDiagnostic `json:"diagnostics,omitempty"`
}

func (e *SkillResolutionError) Error() string {
	if len(e.Diagnostics) == 0 {
		return "skill resolution failed"
	}
	return "skill resolution failed: " + e.Diagnostics[0].Code
}

type ResolvedSkillBinding struct {
	Binding SkillBinding `json:"binding"`
	Version SkillVersion `json:"version"`
}
type ResolvedSkillSet struct {
	Legacy       bool                   `json:"legacy,omitempty"`
	HostRevision uint64                 `json:"host_revision,omitempty"`
	Scope        string                 `json:"scope,omitempty"`
	Config       *SkillConfig           `json:"config,omitempty"`
	Bindings     []ResolvedSkillBinding `json:"bindings"`
	Diagnostics  []SkillDiagnostic      `json:"diagnostics,omitempty"`
}

func selectSkillScope(scopes SkillScopes) (SkillScope, string, []SkillDiagnostic, error) {
	var selected SkillScope
	name := ""
	var warnings []SkillDiagnostic
	ordered := []struct {
		name  string
		scope SkillScope
	}{{"application", scopes.Application}, {"project", scopes.Project}, {"agent", scopes.Agent}, {"workflow", scopes.Workflow}, {"session", scopes.Session}}
	for _, item := range ordered {
		c := item.scope.Config
		if c == nil {
			continue
		}
		if item.name == "project" && !scopes.ProjectTrusted {
			warnings = append(warnings, SkillDiagnostic{Code: "untrusted_source", Scope: "project", Message: "Project skill preferences were not applied.", Remedy: "Review the project before accepting its preferences."})
			continue
		}
		if err := c.Validate(); err != nil {
			return SkillScope{}, "", nil, err
		}
		if c.Configured {
			if len(item.scope.Lock) > maxRegistrySnapshot {
				return SkillScope{}, "", nil, errors.New("skill lock size limit exceeded")
			}
			selected = SkillScope{Config: cloneSkillConfig(c), Lock: append([]byte(nil), item.scope.Lock...)}
			name = item.name
		}
	}
	return selected, name, warnings, nil
}

// FreezeSkillPreferences copies effective defaults for a new session. Later
// application edits cannot change this session's explicit selection or lock.
// No activation or host approval is created by taking this snapshot.
func FreezeSkillPreferences(scopes SkillScopes) (SkillScope, []SkillDiagnostic, error) {
	selected, _, warnings, err := selectSkillScope(scopes)
	return selected, warnings, err
}

func (tx *SkillHostTransaction) ResolveSkills(ctx context.Context, scopes SkillScopes, policy SkillResolutionPolicy) (ResolvedSkillSet, error) {
	if err := tx.check(); err != nil {
		return ResolvedSkillSet{}, err
	}
	if err := ctx.Err(); err != nil {
		return ResolvedSkillSet{}, err
	}
	selected, scope, warnings, err := selectSkillScope(scopes)
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	result := ResolvedSkillSet{Scope: scope, Bindings: []ResolvedSkillBinding{}, Diagnostics: warnings}
	if selected.Config == nil {
		result.Legacy = true
		return result, nil
	}
	if err := policy.Budget.Validate(); err != nil {
		return ResolvedSkillSet{}, errors.New("invalid host skill budget")
	}
	result.Config = selected.Config
	result.Config.Budget = result.Config.Budget.intersect(policy.Budget)
	if policy.RequireControlled {
		result.Config.Enforcement = "controlled"
	}
	if len(result.Config.Bindings) == 0 {
		return result, nil
	}
	lockEntries := make(map[string]SkillVersionLockEntry)
	lock, lockErr := DecodeSkillVersionLock(selected.Lock)
	if lockErr == nil {
		for _, e := range lock.Entries {
			lockEntries[e.Ref] = e
		}
	}
	bindings := append([]SkillBinding(nil), result.Config.Bindings...)
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Ref < bindings[j].Ref })
	var failures []SkillDiagnostic
	for _, binding := range bindings {
		if err := ctx.Err(); err != nil {
			return ResolvedSkillSet{}, err
		}
		if binding.Activation == "off" {
			continue
		}
		fail := func(code, message, remedy string) {
			d := SkillDiagnostic{Code: code, Ref: binding.Ref, Scope: scope, Message: message, Remedy: remedy}
			if binding.Optional {
				result.Diagnostics = append(result.Diagnostics, d)
			} else {
				failures = append(failures, d)
			}
		}
		e, exists := lockEntries[binding.Ref]
		if lockErr != nil || !exists {
			fail("integrity_mismatch", "A valid pinned lock entry is required.", "Review and export a lock for this selection.")
			continue
		}
		v, err := tx.catalogue.Resolve(binding.Ref, e.Digest)
		if err != nil {
			fail("skill_not_found", "The locked skill version is not installed.", "Install the exact locked version; do not substitute another digest.")
			continue
		}
		expected, err := lockEntry(v)
		actualJSON, _ := json.Marshal(e)
		expectedJSON, _ := json.Marshal(expected)
		if err != nil || !bytes.Equal(actualJSON, expectedJSON) {
			fail("integrity_mismatch", "Lock identity or provenance differs from the installed version.", "Review the source and lock before retrying.")
			continue
		}
		if _, _, err := tx.catalogue.versionFiles(ctx, v.Ref, v.Digest); err != nil {
			if ctx.Err() != nil {
				return ResolvedSkillSet{}, ctx.Err()
			}
			fail("integrity_mismatch", "Installed skill content could not be verified.", "Restore the reviewed package before retrying.")
			continue
		}
		if !tx.local.Approved(v) || policy.DeniedSources[v.SourceID] {
			fail("untrusted_source", "The host has not authorized this source and digest.", "Review this exact version in host settings.")
			continue
		}
		transportOK := policy.Transport == "controlled" || (policy.Transport == "compatible" && result.Config.Enforcement == "compatible")
		if !transportOK || policy.IncompatibleRefs[binding.Ref] {
			fail("incompatible_transport", "The adapter cannot satisfy this skill configuration.", "Choose a verified transport or explicitly revise the configuration.")
			continue
		}
		result.Bindings = append(result.Bindings, ResolvedSkillBinding{Binding: binding, Version: v})
	}
	if len(failures) > 0 {
		return ResolvedSkillSet{}, &SkillResolutionError{Diagnostics: append(failures, result.Diagnostics...)}
	}
	return result, nil
}

// ResolveSkills is a read-only host preview. It returns eligibility/identity,
// never a loaded/applied receipt. Identical scopes use this path on every surface.
func (s *HostSkillStore) ResolveSkills(ctx context.Context, scopes SkillScopes, policy SkillResolutionPolicy) (ResolvedSkillSet, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	defer func() { tx.active = false }()
	result, err := tx.ResolveSkills(ctx, scopes, policy)
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	result.HostRevision = head.Revision
	return result, nil
}

// ResolveAndHold commits retention for a validated set under one host lock.
// Failure returns no usable partial set and does not advance the host state.
func (s *HostSkillStore) ResolveAndHold(ctx context.Context, expectedRevision uint64, owner string, scopes SkillScopes, policy SkillResolutionPolicy) (ResolvedSkillSet, error) {
	var result ResolvedSkillSet
	_, err := s.Update(ctx, expectedRevision, func(tx *SkillHostTransaction) error {
		var err error
		result, err = tx.ResolveSkills(ctx, scopes, policy)
		if err != nil {
			return err
		}
		versions := make([]SkillVersion, len(result.Bindings))
		for i, b := range result.Bindings {
			versions[i] = b.Version
		}
		if len(versions) == 0 {
			return nil
		}
		return tx.Hold(ctx, owner, versions)
	})
	if err != nil {
		return ResolvedSkillSet{}, err
	}
	result.HostRevision = expectedRevision + 1
	return result, nil
}
