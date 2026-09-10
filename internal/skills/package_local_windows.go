package skills

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
)

const packageReadFlags = 0

func validatePackageLinkCount(f *os.File, _ os.FileInfo) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("package links/reparse points are not permitted")
	}
	return nil
}
