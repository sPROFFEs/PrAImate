//go:build !linux

package skills

import "os"

// Go does not expose a portable directory flush on Windows. Files are flushed
// individually and publication is atomic for process crashes; power-loss
// durability on Windows is not asserted. Recovery always verifies full content.
func syncPackageDirectory(*os.Root, string) error { return nil }
