package core

import (
	"context"
	"errors"
	"fmt"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

type SkillSelectionInput struct {
	Config *skills.SkillConfig      `json:"config"`
	Lock   *skills.SkillVersionLock `json:"lock"`
}

type InstalledSkillChoice struct {
	Ref        string `json:"ref"`
	Digest     string `json:"digest"`
	Activation string `json:"activation"`
}

// InstalledSkillSummary is the human-facing catalogue record. The immutable
// identity remains available for locking and diagnostics, while normal UI can
// lead with the manifest name and description.
type InstalledSkillSummary struct {
	skills.SkillVersion
	Name        string `json:"name"`
	Description string `json:"description"`
	Approved    bool   `json:"approved"`
}

func InstalledSkillVersions(ctx context.Context) ([]skills.SkillVersion, error) {
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return nil, err
	}
	defer host.Close()
	view, err := host.View(ctx)
	return view.Versions, err
}

func installedSkillSummaries(ctx context.Context, host *skills.HostSkillStore, versions []skills.SkillVersion) ([]InstalledSkillSummary, error) {
	out := make([]InstalledSkillSummary, 0, len(versions))
	for _, version := range versions {
		verified, files, err := host.ReadVersion(ctx, version.Ref, version.Digest)
		if err != nil {
			return nil, err
		}
		var manifest skills.PackageManifest
		for _, file := range files {
			if file.Path == "SKILL.md" {
				manifest, err = skills.ParsePackageManifest(file.Content)
				break
			}
		}
		if err != nil {
			return nil, fmt.Errorf("read manifest for %s: %w", version.Ref, err)
		}
		approved, err := host.VersionApproved(ctx, version.Ref, version.Digest)
		if err != nil {
			return nil, err
		}
		out = append(out, InstalledSkillSummary{
			SkillVersion: verified,
			Name:         manifest.Name,
			Description:  manifest.Description,
			Approved:     approved,
		})
	}
	return out, nil
}

func InstalledSkillSummaries(ctx context.Context) ([]InstalledSkillSummary, error) {
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return nil, err
	}
	defer host.Close()
	view, err := host.View(ctx)
	if err != nil {
		return nil, err
	}
	return installedSkillSummaries(ctx, host, view.Versions)
}

// BuildInstalledSkillSelection constructs a lock from verified installed
// identities. A renderer never supplies provenance, source authority or trust.
func (c *Core) BuildInstalledSkillSelection(ctx context.Context, choices []InstalledSkillChoice) (SkillSelectionInput, error) {
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return SkillSelectionInput{}, err
	}
	if choices == nil || len(choices) > 128 {
		return SkillSelectionInput{}, errors.New("select at most 128 installed versions (use [] for none)")
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return SkillSelectionInput{}, err
	}
	defer host.Close()
	config := &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: []skills.SkillBinding{}, Budget: defaultControlledSkillBudget()}
	versions := make([]skills.SkillVersion, 0, len(choices))
	for _, choice := range choices {
		v, _, err := host.ReadVersion(ctx, choice.Ref, choice.Digest)
		if err != nil {
			return SkillSelectionInput{}, fmt.Errorf("selected skill version is unavailable: %w", err)
		}
		versions = append(versions, v)
		config.Bindings = append(config.Bindings, skills.SkillBinding{Ref: choice.Ref, Activation: choice.Activation})
	}
	if err := config.Validate(); err != nil {
		return SkillSelectionInput{}, err
	}
	body, err := host.ExportLock(ctx, versions)
	if err != nil {
		return SkillSelectionInput{}, err
	}
	lock, err := skills.DecodeSkillVersionLock(body)
	if err != nil {
		return SkillSelectionInput{}, err
	}
	selection := SkillSelectionInput{Config: config, Lock: &lock}
	if _, err := c.PreviewBoundSkills(ctx, nil, nil, ChatSettings{SkillsV2: config, SkillsLock: &lock}); err != nil {
		return SkillSelectionInput{}, err
	}
	return selection, nil
}

func ParseSkillSelectionInput(body []byte) (SkillSelectionInput, error) {
	var v SkillSelectionInput
	if err := skills.DecodePortableSkillRecord(body, &v); err != nil {
		return v, err
	}
	var fields map[string]any
	if err := skills.DecodePortableSkillRecord(body, &fields); err != nil {
		return v, err
	}
	if _, ok := fields["config"]; !ok {
		return v, errors.New("config is required (use null for legacy)")
	}
	if _, ok := fields["lock"]; !ok {
		return v, errors.New("lock is required (use null for legacy)")
	}
	_, err := skills.SelectionScope(v.Config, v.Lock)
	return v, err
}
func (c *Core) PreviewNewBoundSkills(ctx context.Context, a *Agent, w *Workflow, s ChatSettings) (skills.ResolvedSkillSet, error) {
	config, lock, err := c.SkillDefaultsV2(ctx)
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	app, err := skills.SelectionScope(config, lock)
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	if err := validateChatSkills(s); err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	scopes := skills.SkillScopes{Application: app, Session: chatSkillScope(s)}
	if a != nil {
		scopes.Agent, err = skills.SelectionScope(a.Skills, a.SkillsLock)
		if err != nil {
			return skills.ResolvedSkillSet{}, err
		}
	}
	if w != nil {
		scopes.Workflow, err = skills.SelectionScope(w.Skills, w.SkillsLock)
		if err != nil {
			return skills.ResolvedSkillSet{}, err
		}
	}
	frozen, _, err := skills.FreezeSkillPreferences(scopes)
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	if frozen.Config == nil {
		return skills.ResolvedSkillSet{Legacy: true}, nil
	}
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	return PreviewSkillResolution(ctx, scopes, skills.SkillResolutionPolicy{Budget: skills.SkillBudget{CatalogTokens: 1000, BodyTokens: 4000, ResourceTokens: 2000, TotalTokens: 6000, MaxActive: 3, MaxLoadCallsPerTurn: 4, MaxResourceReadsPerTurn: 6, MaxLoadedBytesFallback: 16384}, Transport: "controlled", RequireControlled: true})
}
func (c *Core) guardNewBoundSkills(ctx context.Context, a *Agent, w *Workflow, s ChatSettings) error {
	if (a != nil && a.Skills != nil) || (w != nil && w.Skills != nil) || s.SkillsV2 != nil {
		if err := c.requireSkillsV2Rollout(ctx); err != nil {
			return err
		}
	}
	_, err := c.PreviewNewBoundSkills(ctx, a, w, s)
	// Required failures are returned by the resolver. Optional diagnostics
	// remain visible without turning a successful resolution into a failure.
	return err
}
