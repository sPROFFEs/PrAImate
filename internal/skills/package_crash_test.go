package skills

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestPackageInstallerCrashRecovery(t *testing.T) {
	if directory := os.Getenv("PRAIMATE_SKILL_CRASH_FIXTURE"); directory != "" {
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		candidates := selectionFixture(t)
		plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = installPackages(context.Background(), root, plan, func() error { os.Exit(37); return nil })
		t.Fatalf("crash hook did not exit: %v", err)
	}
	directory := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPackageInstallerCrashRecovery$")
	cmd.Env = append(os.Environ(), "PRAIMATE_SKILL_CRASH_FIXTURE="+directory)
	err := cmd.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 37 {
		t.Fatalf("unexpected child result: %v", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".stage-") {
		t.Fatal("interrupted generation was published")
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, entries[0].Name(), PackageLimits{}); err == nil {
		t.Fatal("orphan exposed as installation")
	}
	candidates := selectionFixture(t)
	plan, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	generation, err := InstallPackages(context.Background(), root, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPackageInstallation(context.Background(), root, generation, PackageLimits{}); err != nil {
		t.Fatal(err)
	}
}

func TestPackageInstallRollbackToPreviousContent(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	candidates := selectionFixture(t)
	a, err := SelectPackages(context.Background(), candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, PackageLimits{})
	if err != nil {
		t.Fatal(err)
	}
	b := []SelectedPackage{{files: a[0].Files()}}
	b[0].files[0].Content = append(b[0].files[0].Content, '\n')
	b[0].digest, err = PackageDigest(b[0].files)
	if err != nil {
		t.Fatal(err)
	}
	first, err := InstallPackages(context.Background(), root, a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := InstallPackages(context.Background(), root, b)
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := InstallPackages(context.Background(), root, a)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || rollback != first {
		t.Fatal("rollback did not resolve original generation")
	}
	for _, generation := range []string{first, second} {
		if _, err := VerifyPackageInstallation(context.Background(), root, generation, PackageLimits{}); err != nil {
			t.Fatal(err)
		}
	}
}
