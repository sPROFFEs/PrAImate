//go:build !windows

package serverboot

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

func hideWindow(*exec.Cmd)   {}
func powershellPath() string { return "powershell.exe" }
func validateRequester(path, uid string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Invalid startup request directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || strconv.FormatUint(uint64(stat.Uid), 10) != uid {
		return errors.New("Startup authorization must belong to the profile's OS account")
	}
	return nil
}
func protect(body []byte) ([]byte, error)   { return append([]byte(nil), body...), nil }
func unprotect(body []byte) ([]byte, error) { return append([]byte(nil), body...), nil }
func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Startup directory must be a real private directory")
	}
	if err := os.Chmod(path, 0700); err != nil {
		return err
	}
	return excludeFromGit(path)
}
func privateWrite(path string, body []byte) error {
	if err := secureDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".startup-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
