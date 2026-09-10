package core

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func TestStoredSkillMigrationIsAdditiveIdempotentAndReversible(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	builtin := BuiltinSkillCatalogue()[0]
	original, err := json.Marshal(map[string]any{"skills": []any{Skill{ID: builtin.ID, Name: "Override", Description: "User override", Body: "EXACT USER BODY"}, map[string]any{"id": "invalid", "body": 42}}})
	if err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "skills.json")
	if err := os.WriteFile(legacy, original, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	draft, err := skills.NewSkillDraft("own-existing", "local/existing", []skills.PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: existing\ndescription: Existing\n---\nExisting")}}, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := s.Update(ctx, 0, func(tx *skills.SkillHostTransaction) error { _, err := tx.Publish(ctx, draft, 1); return err })
	if err != nil {
		t.Fatal(err)
	}
	plan, err := PreviewStoredLegacySkills(ctx, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	override, diagnostic := false, false
	for _, e := range plan.Entries() {
		override = override || e.OverridesBuiltin
		diagnostic = diagnostic || e.Diagnostic != ""
	}
	if !override || !diagnostic {
		t.Fatal("preview lost override or invalid entry")
	}
	if _, err := s.Update(ctx, 1, func(tx *skills.SkillHostTransaction) error { _, err := tx.MigrateLegacy(ctx, plan, false); return err }); err == nil {
		t.Fatal("unacknowledged diagnostics accepted")
	}
	var first, second skills.LegacyMigrationResult
	a, err := s.Update(ctx, 1, func(tx *skills.SkillHostTransaction) error {
		first, err = tx.MigrateLegacy(ctx, plan, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Update(ctx, 2, func(tx *skills.SkillHostTransaction) error {
		second, err = tx.MigrateLegacy(ctx, plan, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if a != b || first.CatalogueSnapshot != second.CatalogueSnapshot || first.OriginalSnapshot != second.OriginalSnapshot {
		t.Fatal("migration is not idempotent")
	}
	view, err := s.View(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Versions) != len(BuiltinSkillCatalogue())+2 || !view.Legacy {
		t.Fatalf("migration lost existing content or switched runtime: %+v", view)
	}
	backup, err := os.ReadFile(filepath.Join(root, "skills-v2", first.OriginalSnapshot))
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("literal backup changed", err)
	}
	_, err = s.Update(ctx, 3, func(tx *skills.SkillHostTransaction) error { return tx.Restore(ctx, baseline) })
	if err != nil {
		t.Fatal(err)
	}
	view, err = s.View(ctx)
	if err != nil || len(view.Versions) != 1 || view.Versions[0].SourceID != "own-existing" || !view.Legacy {
		t.Fatal("rollback failed", err)
	}
	after, err := os.ReadFile(legacy)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatal("legacy file mutated", err)
	}
}

func TestStoredLegacyPreviewRejectsNonregularCatalogue(t *testing.T) {
	root := t.TempDir()
	t.Setenv("PRAIMATE_HOME", root)
	if err := os.Mkdir(filepath.Join(root, "skills.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := PreviewStoredLegacySkills(context.Background(), skills.PackageLimits{}); err == nil {
		t.Fatal("directory accepted as legacy catalogue")
	}
}
