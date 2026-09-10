package skills

import (
	"context"
	"os"
	"testing"
)

func TestLegacyMigrationPreservesOverridesAndOriginal(t *testing.T) {
	ctx := context.Background()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	original := []byte("original catalogue bytes\r\n")
	plan, err := PrepareLegacyMigration(ctx, []LegacyInlineSkill{
		{ID: "same", Name: "Builtin", Description: "Test", Body: "BUILTIN", Builtin: true},
		{ID: "same", Name: "Override", Description: "Test", Body: " USER BODY\r\n", CLIs: []string{"codex"}},
		{ID: "broken", Invalid: "invalid fixture"},
	}, original, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyLegacyMigration(ctx, root, plan, false, PackageLimits{}); err == nil {
		t.Fatal("diagnostics silently ignored")
	}
	first, err := ApplyLegacyMigration(ctx, root, plan, true, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ApplyLegacyMigration(ctx, root, plan, true, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if first.CatalogueSnapshot != second.CatalogueSnapshot || first.OriginalSnapshot != second.OriginalSnapshot {
		t.Fatal("migration not idempotent")
	}
	if !first.Entries[1].OverridesBuiltin || first.Entries[0].SourceID == first.Entries[1].SourceID || first.Entries[2].Diagnostic == "" {
		t.Fatal("override/diagnostic lost")
	}
	backup, err := root.ReadFile(first.OriginalSnapshot)
	if err != nil || string(backup) != string(original) {
		t.Fatal("original bytes lost")
	}
	catalogue, err := LoadVersionCatalogue(ctx, root, first.CatalogueSnapshot, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	versions := catalogue.Versions(first.Entries[1].Ref)
	if len(versions) != 1 {
		t.Fatal("override missing")
	}
	body, err := root.ReadFile(versions[0].Generation + "/objects/" + versions[0].Digest[7:] + "/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := ParsePackageManifest(body)
	if err != nil || manifest.Body != " USER BODY\r\n" || manifest.Metadata["legacy-clis"] != `["codex"]` {
		t.Fatal("legacy content rewritten")
	}
}
