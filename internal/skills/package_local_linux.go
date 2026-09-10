package skills

import (
	"errors"
	"os"
	"syscall"
)

// Nonblocking avoids hanging if a regular entry is replaced with a FIFO.
const packageReadFlags = syscall.O_NONBLOCK | syscall.O_NOFOLLOW

func validatePackageLinkCount(_ *os.File, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("package hardlinks are not permitted")
	}
	return nil
}
