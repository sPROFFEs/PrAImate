package serverboot

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"unsafe"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
func validateRequester(path, uid string) error {
	u, err := user.Current()
	if err != nil || u.Uid != uid {
		return errors.New("Authorize startup with the same Windows account")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Invalid startup request directory")
	}
	return nil
}
func powershellPath() string {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`
	}
	return filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
}
func protect(body []byte) ([]byte, error)   { return crypt(body, true) }
func unprotect(body []byte) ([]byte, error) { return crypt(body, false) }
func crypt(body []byte, encrypt bool) ([]byte, error) {
	if len(body) == 0 {
		return nil, errors.New("Empty startup credential")
	}
	in := windows.DataBlob{Size: uint32(len(body)), Data: &body[0]}
	var out windows.DataBlob
	var err error
	if encrypt {
		err = windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	} else {
		err = windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out)
	}
	if err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	bytes := unsafe.Slice(out.Data, int(out.Size))
	result := append([]byte(nil), bytes...)
	clear(bytes)
	return result, nil
}
func secureDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Startup directory must be a real directory")
	}
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
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
