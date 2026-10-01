package artifacts

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Runtime bundles must contain regular files/directories only. Publishers
// dereference their upstream symlinks; clients never extract links or devices.
func walkArchive(ctx context.Context, payload, format, root string, install bool) error {
	seen := map[string]bool{}
	var total int64
	consume := func(name string, size int64, mode os.FileMode, dir bool, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		name = strings.TrimSuffix(name, "/")
		if !safeRelative(name) || seen[name] {
			return fmt.Errorf("invalid or duplicate runtime path %q", name)
		}
		seen[name] = true
		if len(seen) > 4096 || size < 0 || size > maxArtifactBytes || total+size > maxArtifactBytes {
			return errors.New("runtime extraction exceeds size/file limit")
		}
		total += size
		if mode&os.ModeType != 0 && !dir {
			return errors.New("runtime links and special files are not allowed")
		}
		target := filepath.Join(root, filepath.FromSlash(name))
		if install {
			if dir {
				return os.MkdirAll(target, 0700)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|mode.Perm()&0111)
			if err != nil {
				return err
			}
			n, copyErr := io.Copy(f, io.LimitReader(&contextReader{ctx: ctx, r: r}, size+1))
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if n != size {
				return errors.New("runtime archive size mismatch")
			}
			return nil
		}
		// Reject changes to extracted runtime files even when the package itself
		// is intact; the release archive remains the source of expected bytes.
		for current := target; current != filepath.Dir(root); current = filepath.Dir(current) {
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return errors.New("runtime file replaced by a symbolic link")
			}
		}
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if dir {
			if !info.IsDir() {
				return errors.New("runtime directory replaced")
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() != size {
			return errors.New("extracted runtime size mismatch")
		}
		f, err := os.Open(target)
		if err != nil {
			return err
		}
		defer f.Close()
		expected, actual := sha256.New(), sha256.New()
		if _, err := io.Copy(expected, &contextReader{ctx: ctx, r: r}); err != nil {
			return err
		}
		if _, err := io.Copy(actual, &contextReader{ctx: ctx, r: f}); err != nil {
			return err
		}
		if string(expected.Sum(nil)) != string(actual.Sum(nil)) {
			return errors.New("extracted runtime SHA-256 verification failed")
		}
		return nil
	}
	switch format {
	case "zip":
		z, err := zip.OpenReader(payload)
		if err != nil {
			return err
		}
		defer z.Close()
		for _, f := range z.File {
			r, err := f.Open()
			if err != nil {
				return err
			}
			err = consume(f.Name, int64(f.UncompressedSize64), f.Mode(), f.FileInfo().IsDir(), r)
			r.Close()
			if err != nil {
				return err
			}
		}
	case "tar.gz":
		f, err := os.Open(payload)
		if err != nil {
			return err
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				return errors.New("runtime links and special files are not allowed")
			}
			if err := consume(h.Name, h.Size, h.FileInfo().Mode(), h.Typeflag == tar.TypeDir, tr); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported runtime package format")
	}
	return nil
}
