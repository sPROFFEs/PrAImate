package skills

import (
	"context"
	"testing"
)

func selectionFixture(t *testing.T) []PackageCandidate {
	t.Helper()
	candidates, _, err := inspectPackageFiles(context.Background(), []PackageFile{
		{Path: "repo/a/SKILL.md", Content: []byte("---\nname: alpha\ndescription: Alpha\n---\nBODY")},
		{Path: "repo/a/assets/data.bin", Content: []byte{0, 1, 2}},
		{Path: "repo/LICENSE", Content: []byte("Synthetic license\r\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return candidates
}

func TestSelectPackagesPreservesSharedBytesAndDigest(t *testing.T) {
	candidates := selectionFixture(t)
	selection := PackageSelection{Candidate: 0, ExpectedDigest: candidates[0].Digest, Shared: []SharedAssociation{{Source: "repo/LICENSE", Destination: "LICENSE"}}}
	plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{selection}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].Digest() == candidates[0].Digest {
		t.Fatal("license missing from digest")
	}
	files := plan[0].Files()
	if len(files) != 3 || files[0].Path != "LICENSE" || string(files[0].Content) != "Synthetic license\r\n" {
		t.Fatalf("resources changed: %+v", files)
	}
	files[0].Content[0] = 'X'
	files[0].Path = "mutated"
	if fresh := plan[0].Files(); fresh[0].Path != "LICENSE" || fresh[0].Content[0] != 'S' {
		t.Fatal("plan mutable through preview")
	}
	// Public metadata is only a preview, not the staged source of truth.
	candidates[0].Manifest.Body = "unapproved change"
	again, err := SelectPackages(context.Background(), candidates, []PackageSelection{selection}, PackageLimits{})
	if err != nil || again[0].Digest() != plan[0].Digest() {
		t.Fatal("metadata changed selected bytes")
	}
}

func TestSelectPackagesRejectsInvalidAssociationsAtomically(t *testing.T) {
	for _, assoc := range []SharedAssociation{
		{Source: "missing", Destination: "LICENSE"},
		{Source: "repo/LICENSE", Destination: "../escape"},
		{Source: "repo/LICENSE", Destination: "SKILL.md"},
		{Source: "repo/LICENSE", Destination: "nested/skill.MD"},
		{Source: "repo/LICENSE", Destination: "assets/data.bin"},
		{Source: "repo/LICENSE", Destination: "ASSETS/other"},
	} {
		t.Run(assoc.Destination+assoc.Source, func(t *testing.T) {
			candidates := selectionFixture(t)
			valid := PackageSelection{Candidate: 0, ExpectedDigest: candidates[0].Digest}
			invalid := valid
			invalid.Shared = []SharedAssociation{assoc}
			if plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{invalid}, PackageLimits{}); err == nil || plan != nil {
				t.Fatal("invalid association returned a plan")
			}
			if plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{valid, invalid}, PackageLimits{}); err == nil || plan != nil {
				t.Fatal("partial plan returned")
			}
		})
	}
}

func TestSelectPackagesRequiresMatchingPreviewAndBudget(t *testing.T) {
	candidates := selectionFixture(t)
	selection := PackageSelection{Candidate: 0, ExpectedDigest: "sha256:wrong"}
	if _, err := SelectPackages(context.Background(), candidates, []PackageSelection{selection}, PackageLimits{}); err == nil {
		t.Fatal("mismatch accepted")
	}
	selection.ExpectedDigest = candidates[0].Digest
	if _, err := SelectPackages(context.Background(), candidates, []PackageSelection{selection}, PackageLimits{ExpandedBytes: 1}); err == nil {
		t.Fatal("budget ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := SelectPackages(ctx, candidates, []PackageSelection{selection}, PackageLimits{}); err == nil {
		t.Fatal("cancel ignored")
	}
}
