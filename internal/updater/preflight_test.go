package updater

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUpdatePreflightUsesTargetDirectoryAndLeavesNoFiles(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "praimate")
	if err := os.WriteFile(exe, []byte("fixture"), 0555); err != nil {
		t.Fatal(err)
	}
	if err := checkUpdateDirectory(exe); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 1 {
		t.Fatalf("permission probe left files: %v", files)
	}
	if err := checkUpdateDirectory(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing installation accepted")
	}
}

func TestUpdatePreflightRejectsReadOnlyInstallationThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires an unprivileged POSIX permission fixture")
	}
	dir := t.TempDir()
	exe := filepath.Join(dir, "praimate")
	if err := os.WriteFile(exe, []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "praimate")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0755)
	if err := checkUpdateDirectory(link); err == nil {
		t.Fatal("writable link directory bypassed protected installation")
	}
}
