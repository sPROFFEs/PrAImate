package skills

import (
	"context"
	"os"
	"testing"
)

func TestRegistrySnapshotRoundTripAndRollback(t *testing.T) {
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
	empty, err := catalogue.SaveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	candidates := selectionFixture(t)
	plan, err := SelectPackages(ctx, candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := InstallPackages(ctx, root, plan)
	if err != nil {
		t.Fatal(err)
	}
	version, err := catalogue.Register(ctx, "source-a", "local/alpha", generation, plan[0].Digest())
	if err != nil {
		t.Fatal(err)
	}
	saved, err := catalogue.SaveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := catalogue.SaveSnapshot(ctx)
	if err != nil || again != saved {
		t.Fatal("snapshot not idempotent")
	}
	loaded, err := LoadVersionCatalogue(ctx, root, saved, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := loaded.Resolve(version.Ref, version.Digest)
	if err != nil || actual != version {
		t.Fatal("snapshot lost version")
	}
	rolledBack, err := LoadVersionCatalogue(ctx, root, empty, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack.Versions(version.Ref)) != 0 {
		t.Fatal("rollback changed original checkpoint")
	}
	if err := root.WriteFile(saved, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadVersionCatalogue(ctx, root, saved, PackageLimits{}); err == nil {
		t.Fatal("corrupt snapshot accepted")
	}
}

func TestRegistrySnapshotRejectsImportedTrust(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, body := range []string{
		`{"schema":"praimate.skill-registry/v1","versions":[],"trusted":true}`,
		`{"schema":"praimate.skill-registry/v1","versions":[{"source_id":"x","ref":"local/x","trusted":true}]}`,
	} {
		if _, err := restoreRegistrySnapshot(context.Background(), root, []byte(body), PackageLimits{}); err == nil {
			t.Fatal("unrecognized trust field accepted")
		}
	}
}
