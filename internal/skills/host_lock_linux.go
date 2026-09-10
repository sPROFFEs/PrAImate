package skills

import (
	"os"
	"syscall"
)

func lockSkillHost(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockSkillHost(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
