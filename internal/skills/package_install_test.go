package skills

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestInstallPackagesPreservesResourcesAndRollback(t *testing.T) {
	candidates := selectionFixture(t)
	plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest, Shared: []SharedAssociation{{Source: "repo/LICENSE", Destination: "LICENSE"}}}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	first, err := InstallPackages(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range plan[0].Files() {
		body, err := root.ReadFile(first + "/objects/" + plan[0].Digest()[7:] + "/" + f.Path)
		if err != nil || string(body) != string(f.Content) {
			t.Fatalf("resource mismatch %s: %v", f.Path, err)
		}
	}
	second, err := InstallPackages(context.Background(), root, plan)
	if err != nil || second != first {
		t.Fatalf("non-idempotent install: %s / %v", second, err)
	}
	updated := []SelectedPackage{{files: plan[0].Files()}}
	updated[0].files[0].Content = append(updated[0].files[0].Content, '\n')
	updated[0].digest, err = PackageDigest(updated[0].files)
	if err != nil {
		t.Fatal(err)
	}
	_, err = installPackages(context.Background(), root, updated, func() error { return errors.New("induced interruption before publication") })
	if err == nil {
		t.Fatal("failure ignored")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != first {
		t.Fatal("failed install leaked or modified generation")
	}
	if _, err := os.Stat(filepath.Join(dir, first, "receipt.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, first, PackageLimits{}); err != nil {
		t.Fatal(err)
	}
	// A crash orphan has no publication name and must never be read as installed.
	if err := root.Mkdir(".stage-orphan", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, ".stage-orphan", PackageLimits{}); err == nil {
		t.Fatal("orphan accepted")
	}
	if err := root.WriteFile(first+"/objects/"+plan[0].Digest()[7:]+"/LICENSE", []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, first, PackageLimits{}); err == nil {
		t.Fatal("corruption accepted after restart")
	}
}

func TestConcurrentPackageInstallIsIdempotent(t *testing.T) {
	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	candidates := selectionFixture(t)
	plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan string, 8)
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			generation, err := InstallPackages(context.Background(), root, plan)
			if err != nil {
				failures <- err
			} else {
				results <- generation
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	expected := ""
	for generation := range results {
		if expected == "" {
			expected = generation
		}
		if generation != expected {
			t.Fatal("concurrent callers published different generations")
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("concurrent installs leaked %d directories", len(entries))
	}
}

func TestInstallPackagesRejectsTamperedPlanBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	files := []PackageFile{{Path: "../escape", Content: []byte("unsafe")}}
	if _, err := InstallPackages(context.Background(), root, []SelectedPackage{{digest: "fake", files: files}}); err == nil {
		t.Fatal("tampering accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("wrote before validation")
	}
}
