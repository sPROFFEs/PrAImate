package skills

import (
	"context"
	"os"
	"testing"
)

func TestDraftCheckpointRestoresUnpublishedEdits(t *testing.T) {
	ctx := context.Background()
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	draft, err := NewSkillDraft("own-id", "local/own", []PackageFile{{Path: "SKILL.md", Content: []byte("unfinished")}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	first, err := draft.SaveCheckpoint(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := draft.ReplaceFiles(1, []PackageFile{{Path: "SKILL.md", Content: []byte("second edit")}}); err != nil {
		t.Fatal(err)
	}
	second, err := draft.SaveCheckpoint(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name     string
		revision uint64
		body     string
	}{{first, 1, "unfinished"}, {second, 2, "second edit"}} {
		loaded, err := LoadSkillDraft(ctx, root, fixture.name, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		revision, files := loaded.Snapshot()
		if revision != fixture.revision || string(files[0].Content) != fixture.body {
			t.Fatal("draft checkpoint changed")
		}
	}
}
