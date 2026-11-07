//go:build !windows

package remote

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func LockProfile(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".server-owner.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another PrAImate server owns this profile: %w", err)
	}
	return f, nil
}
