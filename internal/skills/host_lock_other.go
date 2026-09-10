//go:build !linux && !windows

package skills

import (
	"errors"
	"os"
)

func lockSkillHost(*os.File) error {
	return errors.New("skill host lifecycle unsupported on this platform")
}
func unlockSkillHost(*os.File) {}
