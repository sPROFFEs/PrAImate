package skills

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"strings"
)

// InspectPackageDirectory snapshots regular files without executing content.
// Root handles confine traversal; identities and link counts are checked on
// opened handles before reading. Concurrent edits are rejected when detected;
// this is not a filesystem-wide atomic snapshot.
func InspectPackageDirectory(ctx context.Context, directory string, limits PackageLimits) ([]PackageCandidate, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, nil, err
	}
	before, err := os.Lstat(directory)
	if err != nil {
		return nil, nil, err
	}
	if !before.IsDir() {
		return nil, nil, errors.New("package source must be a directory, not a link")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	after, err := root.Stat(".")
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(before, after) {
		return nil, nil, errors.New("package directory changed")
	}
	return inspectPackageRoot(ctx, root, limits)
}

// InspectPackageSubdirectory confines a collection member to an already-open
// source root, including when an ancestor is swapped during inspection.
func InspectPackageSubdirectory(ctx context.Context, source *os.Root, relative string, limits PackageLimits) ([]PackageCandidate, []string, error) {
	if source == nil || validatePackagePath(relative) != nil {
		return nil, nil, errors.New("invalid package subdirectory")
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, nil, err
	}
	before, err := source.Lstat(relative)
	if err != nil || !before.IsDir() {
		return nil, nil, errors.New("package source must be a directory, not a link")
	}
	root, err := source.OpenRoot(relative)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	after, err := root.Stat(".")
	if err != nil || !os.SameFile(before, after) {
		return nil, nil, errors.New("package directory changed")
	}
	return inspectPackageRoot(ctx, root, limits)
}

func inspectPackageRoot(ctx context.Context, root *os.Root, limits PackageLimits) ([]PackageCandidate, []string, error) {
	var files []PackageFile
	inventory := make(packageInventory)
	entries := 0
	var total int64
	var walk func(*os.Root, string) error
	walk = func(dir *os.Root, prefix string) error {
		listing, err := dir.Open(".")
		if err != nil {
			return err
		}
		defer listing.Close()
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch, readErr := listing.ReadDir(1)
			if readErr != nil && readErr != io.EOF {
				return readErr
			}
			for _, entry := range batch {
				entries++
				if entries > limits.Entries {
					return errors.New("package entry limit exceeded")
				}
				name := entry.Name()
				full := name
				if prefix != "" {
					full = prefix + "/" + name
				}
				info, err := dir.Lstat(name)
				if err != nil {
					return err
				}
				if err := inventory.add(full, info.IsDir()); err != nil {
					return err
				}
				if info.IsDir() {
					// Bound recursive stack and open directory handles separately.
					if len(full) > 4096 || strings.Count(full, "/") >= 64 {
						return errors.New("package directory depth/path limit exceeded")
					}
					child, err := dir.OpenRoot(name)
					if err != nil {
						return err
					}
					opened, statErr := child.Stat(".")
					if statErr == nil && !os.SameFile(info, opened) {
						statErr = errors.New("package directory changed")
					}
					if statErr == nil {
						statErr = walk(child, full)
					}
					closeErr := child.Close()
					if statErr != nil {
						return statErr
					}
					if closeErr != nil {
						return closeErr
					}
					continue
				}
				if !info.Mode().IsRegular() {
					return errors.New("non-regular package entry")
				}
				limit := limits.FileBytes
				if path.Base(full) == "SKILL.md" && limit > maxSkillMarkdown {
					limit = maxSkillMarkdown
				}
				if left := limits.ExpandedBytes - total; limit > left {
					limit = left
				}
				body, executable, err := readLocalPackageFile(ctx, dir, name, info, limit)
				if err != nil {
					return err
				}
				total += int64(len(body))
				files = append(files, PackageFile{Path: full, Content: body, Executable: executable})
			}
			if readErr == io.EOF {
				return nil
			}
		}
	}
	if err := walk(root, ""); err != nil {
		return nil, nil, err
	}
	return inspectPackageFiles(ctx, files)
}

func readLocalPackageFile(ctx context.Context, root *os.Root, name string, before os.FileInfo, limit int64) ([]byte, bool, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|packageReadFlags, 0)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, false, errors.New("package file changed or is not regular")
	}
	if err := validatePackageLinkCount(f, opened); err != nil {
		return nil, false, err
	}
	if opened.Size() > limit {
		return nil, false, errors.New("package file limit exceeded")
	}
	body, err := io.ReadAll(io.LimitReader(packageContextReader{ctx, f}, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limit {
		return nil, false, errors.New("package file limit exceeded")
	}
	after, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if after.Size() != opened.Size() || int64(len(body)) != opened.Size() || after.ModTime() != opened.ModTime() || after.Mode() != opened.Mode() {
		return nil, false, errors.New("package file changed during inspection")
	}
	if err := validatePackageLinkCount(f, after); err != nil {
		return nil, false, err
	}
	return body, opened.Mode().Perm()&0111 != 0, nil
}
