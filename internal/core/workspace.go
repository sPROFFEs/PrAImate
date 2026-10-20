package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateWorkspaceDirectory applies the same existing-directory contract to
// initial sessions and workspace changes. It never creates a directory.
func ValidateWorkspaceDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("a working folder is required")
	}
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("resolve working folder: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("working folder is unavailable: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working folder is not a directory")
	}
	return filepath.EvalSymlinks(abs)
}
