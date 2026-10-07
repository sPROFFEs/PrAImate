package updater

import (
	"fmt"
	"os"
	"path/filepath"
)

// CheckPermissions checks the directory used by the atomic replacement, rather
// than requiring root or checking the running binary's write bits. ACLs and
// read-only mounts are respected by an actual temporary-file operation.
func CheckPermissions() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own executable: %w", err)
	}
	return checkUpdateDirectory(exe)
}

func checkUpdateDirectory(exe string) error {
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve update executable: %w", err)
	}
	dir := filepath.Dir(resolved)
	f, err := os.CreateTemp(dir, ".praimate-update-check-*")
	if err != nil {
		return fmt.Errorf("cannot update installation in %s: %w; use an elevated updater or install PrAImate in a user-writable directory", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("check update directory: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("remove update permission probe: %w", err)
	}
	return nil
}
