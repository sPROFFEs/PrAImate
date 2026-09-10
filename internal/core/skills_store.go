package core

import (
	"context"
	"fmt"
	"path/filepath"

	"git.jtsec.local/lab/PrAImate/internal/appdata"
	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// EnsureBuiltinSkillsV2 installs the exact skills shipped in this binary into
// the common immutable registry. Enabling the feature is the host decision
// that approves these built-in bytes; external and agent-pack skills still
// require their own content-bound review.
func EnsureBuiltinSkillsV2(ctx context.Context) error {
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return err
	}
	defer host.Close()
	view, err := host.View(ctx)
	if err != nil {
		return err
	}
	_, err = host.Update(ctx, view.Revision, func(tx *skills.SkillHostTransaction) error {
		for _, builtin := range BuiltinSkillCatalogue() {
			markdown := fmt.Sprintf("---\nname: %q\ndescription: %q\n---\n\n%s\n", builtin.ID, builtin.Description, builtin.Body)
			draft, err := skills.NewSkillDraft("builtin:"+builtin.ID, "builtin/"+builtin.ID, []skills.PackageFile{{Path: "SKILL.md", Content: []byte(markdown)}}, skills.PackageLimits{})
			if err != nil {
				return err
			}
			selected, err := draft.Preview(ctx, 1)
			if err != nil {
				return err
			}
			version, err := tx.RegisterSource(ctx, "builtin:"+builtin.ID, "builtin/"+builtin.ID, selected, skills.SourceProvenance{Kind: "builtin"})
			if err != nil {
				return err
			}
			if err := tx.Approve(ctx, version.Ref, version.Digest, true); err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

// OpenSkillStore is the single host entry point for P2 authoring/registry state.
// It lives beneath the canonical application root (including delete-all-data).
// Opening it neither migrates legacy settings nor enables v2 runtime behavior.
func OpenSkillStore(limits skills.PackageLimits) (*skills.HostSkillStore, error) {
	root, err := appdata.Root()
	if err != nil {
		return nil, err
	}
	return skills.OpenHostSkillStore(filepath.Join(root, "skills-v2"), limits)
}

// PreviewStoredLegacySkills takes a bounded literal snapshot for review. Apply
// this same plan with SkillHostTransaction.MigrateLegacy; do not reread at commit.
// Existing files, default selections and chat/agent settings remain untouched.
func PreviewStoredLegacySkills(ctx context.Context, limits skills.PackageLimits) (*skills.LegacyMigrationPlan, error) {
	root, err := appdata.Root()
	if err != nil {
		return nil, err
	}
	body, err := skills.ReadLegacySkillCatalogue(ctx, root)
	if err != nil {
		return nil, err
	}
	return PreviewLegacySkillMigration(ctx, body, limits)
}
