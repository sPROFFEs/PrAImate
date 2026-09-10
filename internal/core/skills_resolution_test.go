package core

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// Keep the current reader fail-closed until v2 persistence and pack handling
// are implemented together. Parsing a config DTO is not agent-v2 support.
func TestSkillsAgentV2IsNotSilentlyAcceptedByLegacyReader(t *testing.T) {
	body := "schema: praimate.agent/v2\nid: test\nname: Test\ninstructions: Test\nsupports: [claude]\nskills:\n  configured: true\n  bindings: []\n"
	if _, err := ParseAgentYAMLForSchema(strings.NewReader(body), AgentSchema); err == nil || !strings.Contains(err.Error(), "unsupported schema") {
		t.Fatal("legacy reader accepted v2 without persistence", err)
	}
}

func TestCoreSkillPreviewMatchesHostResolverWithoutMutatingState(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PRAIMATE_HOME", t.TempDir())
	store, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	draft, err := skills.NewSkillDraft("own-fixture", "local/test", []skills.PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: test\ndescription: Test\n---\nEXACT BODY")}}, skills.PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var version skills.SkillVersion
	_, err = store.Update(ctx, 0, func(tx *skills.SkillHostTransaction) error {
		version, err = tx.Publish(ctx, draft, 1)
		if err != nil {
			return err
		}
		return tx.Approve(ctx, version.Ref, version.Digest, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := store.ExportLock(ctx, []skills.SkillVersion{version})
	if err != nil {
		t.Fatal(err)
	}
	budget := skills.SkillBudget{CatalogTokens: 100, BodyTokens: 200, ResourceTokens: 100, TotalTokens: 300, MaxActive: 2, MaxLoadCallsPerTurn: 2, MaxResourceReadsPerTurn: 2, MaxLoadedBytesFallback: 1000}
	config := &skills.SkillConfig{Schema: skills.SkillConfigSchema, Configured: true, Lockfile: "skills.lock.json", Enforcement: "controlled", Bindings: []skills.SkillBinding{{Ref: version.Ref, Activation: "pinned"}}, Budget: budget}
	scopes := skills.SkillScopes{Session: skills.SkillScope{Config: config, Lock: lock}}
	policy := skills.SkillResolutionPolicy{Budget: budget, Transport: "controlled"}
	want, err := store.ResolveSkills(ctx, scopes, policy)
	if err != nil {
		t.Fatal(err)
	}
	got, err := PreviewSkillResolution(ctx, scopes, policy)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatal("Core preview diverges", err)
	}
	view, err := store.View(ctx)
	if err != nil || view.Revision != 1 {
		t.Fatal("preview mutated host state", err)
	}
}
