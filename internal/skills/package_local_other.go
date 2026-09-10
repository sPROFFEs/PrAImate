//go:build !linux && !windows

package skills

import (
	"errors"
	"os"
)

const packageReadFlags = 0

func validatePackageLinkCount(*os.File, os.FileInfo) error {
	return errors.New("local package acquisition is unsupported on this platform")
}
