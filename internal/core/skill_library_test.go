package core

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sPROFFEs/PrAImate/internal/skills"
)

func TestSkillLibraryReviewedBulkImportEnablesExactSelection(t *testing.T) {
	c, _, _ := v2AgentFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		body := []byte(fmt.Sprintf("---\nname: %s\ndescription: Bulk import\n---\nOriginal instructions", name))
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	in := SkillLibraryRequest{Action: "inspect", Kind: "directory", Source: root}
	preview, err := c.SkillLibrary(ctx, in)
	if err != nil || len(preview.Packages) != 2 {
		t.Fatalf("bulk preview: %v", err)
	}
	state, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	in.Action, in.Revision, in.Review, in.Approved = "install", state.View.Revision, preview.Review, true
	for _, pkg := range preview.Packages {
		in.Selections = append(in.Selections, SkillLibrarySelection{Index: pkg.Index, Ref: pkg.Ref})
	}
	in.Review = "sha256:stale-review"
	if _, err := c.SkillLibrary(ctx, in); err == nil {
		t.Fatal("enabled content without its exact review")
	}
	in.Review = preview.Review
	invalid := in
	invalid.Action = "inspect"
	invalid.Selections = append([]SkillLibrarySelection(nil), in.Selections...)
	invalid.Selections[1].Ref = "invalid ref"
	badPreview, err := c.SkillLibrary(ctx, invalid)
	if err != nil {
		t.Fatal(err)
	}
	invalid.Action, invalid.Review = "install", badPreview.Review
	if _, err := c.SkillLibrary(ctx, invalid); err == nil {
		t.Fatal("invalid second alias did not reject the batch")
	}
	if _, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: preview.Packages[0].Ref, Digest: preview.Packages[0].Digest}); err == nil {
		t.Fatal("failed batch left its first version installed or enabled")
	}
	if _, err := c.SkillLibrary(ctx, in); err != nil {
		t.Fatal(err)
	}
	var choices []InstalledSkillChoice
	for _, pkg := range preview.Packages {
		installed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: pkg.Ref, Digest: pkg.Digest})
		if err != nil || !installed.Approved {
			t.Fatalf("reviewed version was not enabled: %s, %v", pkg.Ref, err)
		}
		choices = append(choices, InstalledSkillChoice{Ref: pkg.Ref, Digest: pkg.Digest, Activation: "auto"})
	}
	selection, err := c.BuildInstalledSkillSelection(ctx, choices)
	if err != nil || len(selection.Config.Bindings) != 2 {
		t.Fatalf("imported skills require another approval: %v", err)
	}
	// A new digest must not inherit the approval of the imported snapshot.
	if err := os.WriteFile(filepath.Join(root, "first", "SKILL.md"), []byte("---\nname: first\ndescription: Bulk import\n---\nChanged instructions"), 0600); err != nil {
		t.Fatal(err)
	}
	state, err = c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = state.View.Revision
	if _, err := c.SkillLibrary(ctx, in); err == nil {
		t.Fatal("changed content inherited the old review and approval")
	}
}

func TestSkillLibraryGitHubFolderImport(t *testing.T) {
	if os.Getenv("PRAIMATE_TEST_GITHUB_SKILL_IMPORT") != "1" {
		t.Skip("set PRAIMATE_TEST_GITHUB_SKILL_IMPORT=1 to inspect and import the public GitHub skill")
	}
	c, _, _ := v2AgentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	in := SkillLibraryRequest{Action: "inspect", Kind: "github", Source: "https://github.com/mukul975/Anthropic-Cybersecurity-Skills/tree/main/skills/conducting-api-security-testing"}
	preview, err := c.SkillLibrary(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Packages) != 1 || len(preview.Shared) != 0 || preview.Source != "https://github.com/mukul975/Anthropic-Cybersecurity-Skills" || preview.Subpath != "skills/conducting-api-security-testing" || len(preview.GitRef) != 40 {
		t.Fatalf("unexpected scoped inspection: packages=%d shared=%v source=%q subpath=%q revision=%q", len(preview.Packages), preview.Shared, preview.Source, preview.Subpath, preview.GitRef)
	}
	checkFiles := func(files []skills.PackageFile) {
		t.Helper()
		want := []string{"LICENSE", "SKILL.md", "references/api-reference.md", "scripts/agent.py"}
		if len(files) != len(want) {
			t.Fatalf("files=%d, want %d", len(files), len(want))
		}
		for i, file := range files {
			if file.Path != want[i] || len(file.Content) == 0 {
				t.Fatalf("file %d=%q, want %q with content", i, file.Path, want[i])
			}
		}
	}
	checkFiles(preview.Packages[0].Files)
	state, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	in.Source, in.GitRef, in.Subpath = preview.Source, preview.GitRef, preview.Subpath
	in.Action, in.Revision, in.Review = "install", state.View.Revision, preview.Review
	in.Selections = []SkillLibrarySelection{{Index: preview.Packages[0].Index, Ref: preview.Packages[0].Ref}}
	if _, err := c.SkillLibrary(ctx, in); err != nil {
		t.Fatal(err)
	}
	installed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: preview.Packages[0].Ref, Digest: preview.Packages[0].Digest})
	if err != nil {
		t.Fatal(err)
	}
	checkFiles(installed.Files)
	if installed.Approved {
		t.Fatal("import granted approval")
	}
	t.Logf("inspected and imported 1 skill with 4 files, 0 shared resources, at %s", preview.GitRef)
}

func TestSkillLibraryLargeRepositoryInspectionAndSelectiveInstall(t *testing.T) {
	c, _, _ := v2AgentFixture(t)
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "catalogue.zip")
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for i := 0; i < 818; i++ {
		for name, content := range map[string]string{
			"SKILL.md":               fmt.Sprintf("---\nname: skill-%04d\ndescription: Catalogue regression fixture\n---\nSkill instructions", i),
			"references/example.txt": strings.Repeat("resource\n", 1400),
		} {
			entry, err := z.Create(fmt.Sprintf("repo/skills/skill-%04d/%s", i, name))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	in := SkillLibraryRequest{Action: "inspect", Kind: "zip", Source: source}
	preview, err := c.SkillLibrary(ctx, in)
	if err != nil || len(preview.Packages) != 818 {
		t.Fatalf("catalogue inspection: packages=%d err=%v", len(preview.Packages), err)
	}
	// The catalogue exceeds both editor limits, but each skill fits them.
	var size int
	for _, pkg := range preview.Packages {
		for _, file := range pkg.Files {
			size += len(file.Content)
		}
	}
	if size <= 8<<20 || libraryLimits().Entries != 1000 || libraryLimits().ExpandedBytes != 8<<20 {
		t.Fatal("fixture must exceed unchanged editor limits")
	}
	in.Selections = []SkillLibrarySelection{{Index: 417, Ref: "test/catalogue-skill"}}
	preview, err = c.SkillLibrary(ctx, in)
	if err != nil || len(preview.Packages) != 1 || len(preview.Packages[0].Files) != 2 {
		t.Fatalf("selection inspection: %+v err=%v", preview, err)
	}
	listed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "list"})
	if err != nil {
		t.Fatal(err)
	}
	in.Action, in.Revision, in.Review = "install", listed.View.Revision, preview.Review
	if _, err := c.SkillLibrary(ctx, in); err != nil {
		t.Fatal(err)
	}
	installed, err := c.SkillLibrary(ctx, SkillLibraryRequest{Action: "read", Ref: "test/catalogue-skill", Digest: preview.Packages[0].Digest})
	if err != nil || len(installed.Files) != 2 || installed.Approved {
		t.Fatalf("selected package resources/trust: files=%d approved=%v err=%v", len(installed.Files), installed.Approved, err)
	}
}

func TestSkillLibraryInspectionKeepsPerSkillEditorLimit(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: large\ndescription: Too large\n---\nInstructions"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("resource-%d.txt", i)), []byte(strings.Repeat("x", 3<<20)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := inspectLibrarySource(context.Background(), SkillLibraryRequest{Kind: "directory", Source: source})
	if err == nil || !strings.Contains(err.Error(), "exceeds editor limits") {
		t.Fatalf("oversized individual skill must be rejected: %v", err)
	}
}

func TestSkillLibraryDownloadedRepositoryCompatibility(t *testing.T) {
	source := os.Getenv("PRAIMATE_TEST_SKILL_ARCHIVE")
	if source == "" {
		t.Skip("set PRAIMATE_TEST_SKILL_ARCHIVE to inspect a downloaded repository without installing it")
	}
	preview, _, err := inspectLibrarySource(context.Background(), SkillLibraryRequest{Kind: "zip", Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Packages) == 0 {
		t.Fatal("no skills found")
	}
	t.Logf("Inspected %d skill packages", len(preview.Packages))
	selected, _, err := inspectLibrarySource(context.Background(), SkillLibraryRequest{Kind: "zip", Source: source, Selections: []SkillLibrarySelection{{Index: len(preview.Packages) / 2}}})
	if err != nil || len(selected.Packages) != 1 {
		t.Fatalf("single selection: packages=%d err=%v", len(selected.Packages), err)
	}
}

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
