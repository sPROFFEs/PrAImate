//go:build !windows

package remote

import (
	"errors"
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
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %w", ErrProfileOwned, err)
		}
		return nil, fmt.Errorf("lock Server profile: %w", err)
	}
	return f, nil
}
