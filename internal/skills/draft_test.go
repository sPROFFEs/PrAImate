package skills

import (
	"context"
	"os"
	"testing"
)

func TestDraftPublishDoesNotMutatePinnedVersion(t *testing.T) {
	ctx := context.Background()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	catalogue, err := NewVersionCatalogue(root, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	files := []PackageFile{{Path: "SKILL.md", Content: []byte("---\nname: own\ndescription: Own skill\n---\nFIRST")}}
	draft, err := NewSkillDraft("local-identity", "local/own", files, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	files[0].Content[0] = 'X' // caller-owned buffers cannot edit a draft.
	first, err := draft.Publish(ctx, catalogue, 1)
	if err != nil {
		t.Fatal(err)
	}
	revision, editable := draft.Snapshot()
	editable[0].Content = append(editable[0].Content, []byte(" SECOND")...)
	next, err := draft.ReplaceFiles(revision, editable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := draft.ReplaceFiles(revision, editable); err == nil {
		t.Fatal("stale save accepted")
	}
	if _, err := draft.Publish(ctx, catalogue, revision); err == nil {
		t.Fatal("stale preview published")
	}
	second, err := draft.Publish(ctx, catalogue, next)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest == second.Digest {
		t.Fatal("edit did not create a new version")
	}
	pinned, err := catalogue.Resolve(first.Ref, first.Digest)
	if err != nil || pinned != first {
		t.Fatal("pinned version mutated")
	}
	if _, err := VerifyPackageInstallation(ctx, root, first.Generation, PackageLimits{}); err != nil {
		t.Fatal(err)
	}
}

func TestDraftInvalidMarkdownCannotPublish(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	catalogue, err := NewVersionCatalogue(root, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := NewSkillDraft("identity", "local/own", []PackageFile{{Path: "SKILL.md", Content: []byte("unfinished draft")}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := draft.Publish(context.Background(), catalogue, 1); err == nil {
		t.Fatal("invalid draft published")
	}
	if len(catalogue.Versions("local/own")) != 0 {
		t.Fatal("invalid draft registered")
	}
}
