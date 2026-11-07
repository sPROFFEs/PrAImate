//go:build windows

package remote

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
)

func LockProfile(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".server-owner.lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("%w: %w", ErrProfileOwned, err)
		}
		return nil, fmt.Errorf("lock Server profile: %w", err)
	}
	return f, nil
}
