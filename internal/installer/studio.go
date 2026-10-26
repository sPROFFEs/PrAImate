package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ResolvePraimateBinary finds the backend shipped alongside the GUI. Studio's
// filesystem-only setup command does not open or unlock the application DB.
func ResolvePraimateBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	name := "praimate"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if strings.EqualFold(filepath.Base(exe), name) {
		return exe, nil
	}
	sibling := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() {
		return sibling, nil
	}
	if found, err := exec.LookPath(name); err == nil {
		return found, nil
	}
	return "", fmt.Errorf("PrAImate backend executable is missing; expected %s", sibling)
}

func installStudio(ctx context.Context, stdout, stderr io.Writer) error {
	backend, err := ResolvePraimateBinary()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, backend, "studio", "install")
	hideConsole(cmd)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("install Studio integration: %w", err)
	}
	return nil
}
