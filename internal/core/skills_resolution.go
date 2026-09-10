package core

import (
	"context"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// PreviewSkillResolution is the shared Core facade for the P3 resolver. The
// host supplies policy/capabilities; callers must not deserialize those from
// agent YAML or project files. This does not change legacy execution routes.
func PreviewSkillResolution(ctx context.Context, scopes skills.SkillScopes, policy skills.SkillResolutionPolicy) (skills.ResolvedSkillSet, error) {
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return skills.ResolvedSkillSet{}, err
	}
	defer store.Close()
	return store.ResolveSkills(ctx, scopes, policy)
}
