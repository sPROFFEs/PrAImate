package studio

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Validate member names for both platforms, regardless of the build host.
// OpenRoot also prevents a preceding symlink from escaping the install.
func archiveName(name string) (string, error) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimSuffix(name, "/")
	if name == "" || name == "." {
		return ".", nil
	}
	if !filepath.IsLocal(name) || strings.Contains(name, ":") {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("unsafe archive path %q", name)
		}
	}
	return filepath.FromSlash(name), nil
}
func copyArchiveFile(root *os.Root, name string, mode os.FileMode, src io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0755); err != nil {
		return err
	}
	if mode.Perm() == 0 {
		mode = 0644
	}
	f, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
func extractTar(src io.Reader, dest string) error {
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	gz, err := gzip.NewReader(src)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name, err := archiveName(hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0755)
		case tar.TypeReg, tar.TypeRegA:
			err = copyArchiveFile(root, name, os.FileMode(hdr.Mode), tr)
		case tar.TypeSymlink:
			target := filepath.Join(filepath.Dir(name), filepath.FromSlash(hdr.Linkname))
			if _, err = archiveName(target); err != nil {
				return err
			}
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("absolute archive link %q", hdr.Linkname)
			}
			if err = root.MkdirAll(filepath.Dir(name), 0755); err == nil {
				err = root.Symlink(hdr.Linkname, name)
			}
		}
		if err != nil {
			return err
		}
	}
}
func extractZip(src io.Reader, dest string) error {
	tmp, err := os.CreateTemp(dest, "studio-download-*.zip")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	_, copyErr := io.Copy(tmp, src)
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	archive, err := zip.OpenReader(tmpName)
	if err != nil {
		return err
	}
	defer archive.Close()
	root, err := os.OpenRoot(dest)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range archive.File {
		name, err := archiveName(entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err = root.MkdirAll(name, 0755); err != nil {
				return err
			}
			continue
		}
		if entry.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsupported ZIP symlink %q", entry.Name)
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		err = copyArchiveFile(root, name, entry.Mode(), input)
		closeErr := input.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
