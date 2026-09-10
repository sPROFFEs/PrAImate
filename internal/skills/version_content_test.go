package skills

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestHostUpdateForkExportAndRevocation(t *testing.T) {
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := hostFixtureDraft(t)
	revision, files := d.Snapshot()
	files = append(files, PackageFile{Path: "LICENSE", Content: []byte("fixture license")}, PackageFile{Path: "scripts/check.sh", Content: []byte("echo old"), Executable: true})
	revision, err = d.ReplaceFiles(revision, files)
	if err != nil {
		t.Fatal(err)
	}
	var first, second, forked SkillVersion
	approvedCheckpoint, err := s.Update(ctx, 0, func(tx *SkillHostTransaction) error {
		first, err = tx.Publish(ctx, d, revision)
		if err != nil {
			return err
		}
		return tx.Approve(ctx, first.Ref, first.Digest, true)
	})
	if err != nil {
		t.Fatal(err)
	}
	files[0].Content = append(files[0].Content, []byte(" NEW")...)
	files[2].Content = []byte("echo new")
	files[2].Executable = false
	revision, err = d.ReplaceFiles(revision, files)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := d.Preview(ctx, revision)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := s.PreviewUpdate(ctx, first.Ref, first.Digest, selected)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Changes) != 2 || diff.Changes[0].Path != "SKILL.md" || diff.Changes[1].Path != "scripts/check.sh" || !diff.Changes[1].Before.Executable || diff.Changes[1].After.Executable {
		t.Fatalf("incomplete diff: %+v", diff)
	}
	_, err = s.Update(ctx, 1, func(tx *SkillHostTransaction) error {
		second, err = tx.Publish(ctx, d, revision)
		if err != nil {
			return err
		}
		if tx.local.Approved(second) {
			return errors.New("update inherited approval")
		}
		fork, err := tx.Fork(ctx, first.Ref, first.Digest, "local/fork")
		if err != nil {
			return err
		}
		if err := tx.SaveDraft(ctx, "fork", fork); err != nil {
			return err
		}
		fork, err = tx.LoadDraft(ctx, "fork")
		if err != nil {
			return err
		}
		forked, err = tx.Publish(ctx, fork, 1)
		if err != nil {
			return err
		}
		if forked.SourceID == first.SourceID || forked.DerivedSource != first.SourceID || forked.DerivedDigest != first.Digest || tx.local.Approved(forked) {
			return errors.New("fork identity/provenance/authority invalid")
		}
		return tx.Approve(ctx, first.Ref, first.Digest, false)
	})
	if err != nil {
		t.Fatal(err)
	}
	v, old, err := s.ReadVersion(ctx, first.Ref, first.Digest)
	if err != nil || v != first || bytes.Contains(old[0].Content, []byte(" NEW")) {
		t.Fatal("pinned version mutated", err)
	}
	// Synthetic host secret must never enter the package archive.
	if err := s.root.WriteFile("credentials.json", []byte("HOST_SECRET_123"), 0600); err != nil {
		t.Fatal(err)
	}
	archive, err := s.ExportPackageZIP(ctx, first.Ref, first.Digest)
	if err != nil {
		t.Fatal(err)
	}
	candidates, _, err := InspectPackageZIP(ctx, bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Digest != first.Digest {
		t.Fatal("export changed bytes or executable intent")
	}
	for _, file := range candidates[0].files {
		if bytes.Contains(file.Content, []byte("HOST_SECRET_123")) || strings.Contains(file.Path, "local-state") {
			t.Fatal("host authority leaked")
		}
	}
	_, err = s.Update(ctx, 2, func(tx *SkillHostTransaction) error {
		if err := tx.Restore(ctx, approvedCheckpoint); err != nil {
			return err
		}
		if tx.local.Approved(first) {
			return errors.New("rollback resurrected approval")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadVersion(ctx, second.Ref, second.Digest); err == nil {
		t.Fatal("rollback retained newer active registry entry")
	}
}

func TestProvenanceValidationAndSourceIdentity(t *testing.T) {
	for _, origin := range []string{"https://user:secret@example.com/repo", "https://example.com/repo?token=SECRET", "https://example.com/repo#SECRET", "http://example.com/repo"} {
		if _, err := ExternalSkillSourceID(origin, ""); err == nil {
			t.Fatal("unsafe origin accepted")
		}
	}
	a, err := ExternalSkillSourceID("https://EXAMPLE.com/repo", "skills/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ExternalSkillSourceID("https://example.com/repo", "skills/a")
	if err != nil || a != b {
		t.Fatal("unstable source identity")
	}
	c, err := ExternalSkillSourceID("https://example.com/repo", "skills/b")
	if err != nil || a == c {
		t.Fatal("subpaths collapsed")
	}
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	selected, err := hostFixtureDraft(t).Preview(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	provenance := SourceProvenance{Kind: "external", Origin: "https://example.com/repo", Subpath: "skills/a", ResolvedRevision: strings.Repeat("a", 40)}
	_, err = s.Update(ctx, 0, func(tx *SkillHostTransaction) error {
		_, err := tx.RegisterSource(ctx, a, "external/test", selected, provenance)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.View(ctx)
	if err != nil || view.Versions[0].Provenance() != provenance {
		t.Fatal("provenance lost on restart", err)
	}
	_, err = s.Update(ctx, 1, func(tx *SkillHostTransaction) error {
		changed := provenance
		changed.ResolvedRevision = strings.Repeat("b", 40)
		version, err := tx.RegisterSource(ctx, a, "external/test", selected, changed)
		if err == nil && version.Provenance() != provenance {
			return errors.New("original provenance overwritten")
		}
		return err
	})
	if err != nil {
		t.Fatal("identical content reimport failed", err)
	}
	_, err = s.Update(ctx, 2, func(tx *SkillHostTransaction) error {
		changed := provenance
		changed.Origin = "https://example.com/another"
		_, err := tx.RegisterSource(ctx, a, "external/test", selected, changed)
		return err
	})
	if err == nil {
		t.Fatal("source origin rebound")
	}
}

func TestHostGCFailsClosedForEveryRetainedAuthority(t *testing.T) {
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v SkillVersion
	_, err = s.Update(ctx, 0, func(tx *SkillHostTransaction) error { v, err = tx.Publish(ctx, hostFixtureDraft(t), 1); return err })
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Update(ctx, 1, func(tx *SkillHostTransaction) error { return tx.Approve(ctx, v.Ref, v.Digest, true) })
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := s.checkpoint(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	// Same catalogue, different authority checkpoint: both must be validated.
	if err := s.root.WriteFile(checkpoint.LocalState, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Collect(ctx, 2, true); err == nil {
		t.Fatal("GC skipped second authority checkpoint")
	}
}

func TestReadVersionRejectsModifiedResource(t *testing.T) {
	ctx := context.Background()
	s, err := OpenHostSkillStore(t.TempDir(), PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v SkillVersion
	_, err = s.Update(ctx, 0, func(tx *SkillHostTransaction) error { v, err = tx.Publish(ctx, hostFixtureDraft(t), 1); return err })
	if err != nil {
		t.Fatal(err)
	}
	name := v.Generation + "/objects/" + v.Digest[7:] + "/SKILL.md"
	f, err := s.root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("substituted")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ExportPackageZIP(ctx, v.Ref, v.Digest); err == nil {
		t.Fatal("tampered resource exported")
	}
}
