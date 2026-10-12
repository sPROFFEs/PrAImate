//go:build !linux && !darwin && !windows

package skills

import (
	"errors"
	"os"
)

func lockSkillHost(*os.File) error {
	return errors.New("skill host lifecycle unsupported on this platform")
}
func unlockSkillHost(*os.File)          {}
func skillHostLockContended(error) bool { return false }
