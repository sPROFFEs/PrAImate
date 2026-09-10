package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

func TestSkillLibraryDraftPublicationPreservesPinnedVersionAndTrust(t *testing.T) {
	c, _, old := v2AgentFixture(t)
	ctx := context.Background()
	call := func(in SkillLibraryRequest) SkillLibraryResult {
		t.Helper()
		listed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		in.Revision = listed.View.Revision
		out, err := c.SkillLibrary(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	created := call(SkillLibraryRequest{Action: "edit", Ref: old.Ref, Digest: old.Digest})
	draft := call(SkillLibraryRequest{Action: "draft-read", Key: created.Key})
	for i := range draft.Files {
		if draft.Files[i].Path == "SKILL.md" {
			draft.Files[i].Content = append(draft.Files[i].Content, []byte("\nNEW REVISION")...)
		}
	}
	call(SkillLibraryRequest{Action: "draft-save", Key: created.Key, Files: draft.Files, DraftRevision: draft.DraftRevision})
	preview := call(SkillLibraryRequest{Action: "draft-preview", Key: created.Key, Ref: old.Ref, Digest: old.Digest})
	if preview.Changes == nil || len(preview.Changes.Changes) != 1 {
		t.Fatal("update lacks exact diff")
	}
	newVersion := call(SkillLibraryRequest{Action: "publish", Key: created.Key, Review: preview.Review}).Version
	if newVersion.Digest == old.Digest || newVersion.SourceID != old.SourceID {
		t.Fatal("publication changed identity or failed to create version")
	}
	original := call(SkillLibraryRequest{Action: "read", Ref: old.Ref, Digest: old.Digest})
	for _, f := range original.Files {
		if strings.Contains(string(f.Content), "NEW REVISION") {
			t.Fatal("published version mutated")
		}
	}
	updated := call(SkillLibraryRequest{Action: "read", Ref: old.Ref, Digest: newVersion.Digest})
	if updated.Approved || len(updated.Files) != 3 {
		t.Fatal("approval inherited or resources lost")
	}
	call(SkillLibraryRequest{Action: "approve", Ref: old.Ref, Digest: newVersion.Digest, Review: newVersion.Digest, Approved: true})
	if !call(SkillLibraryRequest{Action: "read", Ref: old.Ref, Digest: newVersion.Digest}).Approved {
		t.Fatal("approval not persisted")
	}
	list := call(SkillLibraryRequest{Action: "list"})
	_, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "draft-save", Revision: list.View.Revision - 1, Key: created.Key, Files: draft.Files})
	if err == nil {
		t.Fatal("stale editor overwrote draft")
	}
	_, err = c.SkillLibrary(ctx, SkillLibraryRequest{Action: "draft-save", Revision: list.View.Revision, Key: created.Key, Files: draft.Files, DraftRevision: draft.DraftRevision})
	if err == nil {
		t.Fatal("refreshing the library bypassed stale draft revision protection")
	}
}

func TestSkillLibraryLegacyCopyIsReviewedIdempotentAndDoesNotSelect(t *testing.T) {
	c, _, _ := v2AgentFixture(t)
	ctx := context.Background()
	preview, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "legacy-inspect"})
	if err != nil || len(preview.LegacyPackages) == 0 {
		t.Fatal("no built-in review", err)
	}
	for i := 0; i < 2; i++ {
		list, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "legacy-migrate", Revision: list.View.Revision, Review: preview.Review}); err != nil {
			t.Fatal(err)
		}
	}
	config, lock, err := c.SkillDefaultsV2(ctx)
	if err != nil || config != nil || lock != nil {
		t.Fatal("migration selected skills", err)
	}
	for ref := range preview.LegacyPackages {
		list, _ := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
		count := 0
		for _, v := range list.View.Versions {
			if v.Ref == ref {
				count++
				result, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: ref, Digest: v.Digest})
				if err != nil || result.Approved {
					t.Fatal("migration granted trust", err)
				}
			}
		}
		if count != 1 {
			t.Fatal("migration not idempotent", ref, count)
		}
	}
}

func TestSkillLibraryImportReviewRejectsChangedSourceAndPreservesResources(t *testing.T) {
	c, _, _ := v2AgentFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	markdown := []byte("---\nname: example\ndescription: Test import\n---\nOriginal")
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), markdown, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("test license"), 0600); err != nil {
		t.Fatal(err)
	}
	in := SkillLibraryRequest{Action: "inspect", Kind: "directory", Source: root, Selections: []SkillLibrarySelection{{Index: 0, Ref: "test/imported"}}}
	p, err := c.SkillLibrary(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Packages) != 1 || len(p.Packages[0].Files) != 2 {
		t.Fatal("resources lost in preview")
	}
	v, _ := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	in.Action, in.Revision, in.Review = "install", v.View.Revision, p.Review
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), append(markdown, []byte(" changed")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SkillLibrary(ctx, in); err == nil {
		t.Fatal("changed source installed under previous review")
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), markdown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SkillLibrary(ctx, in); err != nil {
		t.Fatal(err)
	}
	read, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: "test/imported", Digest: p.Packages[0].Digest})
	if err != nil || read.Approved {
		t.Fatal("import granted approval", err)
	}
	listed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	if err != nil || len(listed.Summaries) == 0 {
		t.Fatal("installed skill summaries missing", err)
	}
	foundSummary := false
	for _, summary := range listed.Summaries {
		if summary.Ref == "test/imported" {
			foundSummary = summary.Name == "example" && summary.Description == "Test import" && !summary.Approved
		}
	}
	if !foundSummary {
		t.Fatalf("manifest metadata missing from installed summary: %#v", listed.Summaries)
	}
	if _, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "edit", Revision: in.Revision + 1, Ref: "test/imported", Digest: read.Version.Digest}); err == nil {
		t.Fatal("imported procedure edited without fork")
	}
	// A forged package cannot carry a local approval through publication.
	if _, err := skills.ParsePackageManifest(markdown); err != nil {
		t.Fatal(err)
	}
}
