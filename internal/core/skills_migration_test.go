package core

import (
	"context"
	"git.jtsec.local/lab/PrAImate/internal/skills"
	"testing"
)

func TestLegacyMigrationPreviewIncludesBuiltinsAndBadUserEntries(t *testing.T) {
	raw := []byte(`{"skills":[{"id":"own","name":"Own","description":"Test","body":"EXACT"},42]}`)
	plan, err := PreviewLegacySkillMigration(context.Background(), raw, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	entries := plan.Entries()
	if len(entries) != len(BuiltinSkillCatalogue())+2 {
		t.Fatal("legacy entries dropped")
	}
	if entries[len(entries)-1].Diagnostic == "" {
		t.Fatal("invalid user entry silently skipped")
	}
	for _, entry := range entries[:len(BuiltinSkillCatalogue())] {
		if entry.Diagnostic != "" {
			t.Fatalf("built-in failed migration preview: %s: %s", entry.LegacyID, entry.Diagnostic)
		}
	}
	if _, err := PreviewLegacySkillMigration(context.Background(), []byte("bad JSON"), skills.PackageLimits{}); err == nil {
		t.Fatal("malformed catalogue ignored")
	}
}
