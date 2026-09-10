package skills

import (
	"context"
	"os"
	"testing"
)

func TestVersionCatalogueSeparatesSourcesAndPinsVersions(t *testing.T) {
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
	candidates := selectionFixture(t)
	plan, err := SelectPackages(ctx, candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := InstallPackages(ctx, root, plan)
	if err != nil {
		t.Fatal(err)
	}
	a, err := catalogue.Register(ctx, "origin-a", "local/alpha", generation, plan[0].Digest())
	if err != nil {
		t.Fatal(err)
	}
	b, err := catalogue.Register(ctx, "origin-b", "external/alpha", generation, plan[0].Digest())
	if err != nil {
		t.Fatal(err)
	}
	if a.SourceID == b.SourceID {
		t.Fatal("same named content merged sources")
	}
	if _, err := catalogue.Register(ctx, "origin-b", "LOCAL/ALPHA", generation, plan[0].Digest()); err == nil {
		t.Fatal("ref collision overwrote source")
	}
	updated := []SelectedPackage{{files: plan[0].Files()}}
	updated[0].files[0].Content = append(updated[0].files[0].Content, '\n')
	updated[0].digest, err = PackageDigest(updated[0].files)
	if err != nil {
		t.Fatal(err)
	}
	nextGeneration, err := InstallPackages(ctx, root, updated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.Register(ctx, "origin-a", "local/alpha", nextGeneration, updated[0].Digest()); err != nil {
		t.Fatal(err)
	}
	pinned, err := catalogue.Resolve("local/alpha", a.Digest)
	if err != nil || pinned != a {
		t.Fatalf("pinned version changed: %+v %v", pinned, err)
	}
	versions := catalogue.Versions("local/alpha")
	if len(versions) != 2 {
		t.Fatal("old version lost")
	}
	versions[0].Digest = "mutated"
	if _, err := catalogue.Resolve("local/alpha", "mutated"); err == nil {
		t.Fatal("external mutation changed registry")
	}
	if _, err := catalogue.Resolve("local/alpha", ""); err == nil {
		t.Fatal("implicit latest allowed")
	}
}

func TestVersionCatalogueRejectsUninstalledContent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	catalogue, err := NewVersionCatalogue(root, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalogue.Register(context.Background(), "source", "local/test", "missing", "sha256:missing"); err == nil {
		t.Fatal("unverified version registered")
	}
	if len(catalogue.Versions("local/test")) != 0 {
		t.Fatal("failed registration changed catalogue")
	}
}
