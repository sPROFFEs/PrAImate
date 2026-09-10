package skills

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// PackageCandidate is an inspection snapshot. Its bytes are private so UI
// selection cannot replace inspected content before a later installation.
// No trust, execution permission or activation is conferred by inspection.
type PackageCandidate struct {
	Subpath  string
	Manifest PackageManifest
	Digest   string
	files    []PackageFile
	shared   map[string]PackageFile
}

// PackageLimits is host policy, never decoded from imported content. Zero
// fields use defaults; negative values are configuration errors.
type PackageLimits struct {
	CompressedBytes int64
	ExpandedBytes   int64
	FileBytes       int64
	Entries         int
}

func (l PackageLimits) normalized() (PackageLimits, error) {
	if l.CompressedBytes < 0 || l.ExpandedBytes < 0 || l.FileBytes < 0 || l.Entries < 0 {
		return l, errors.New("invalid package limits")
	}
	if l.CompressedBytes == 0 {
		l.CompressedBytes = 25 << 20
	}
	if l.ExpandedBytes == 0 {
		l.ExpandedBytes = 100 << 20
	}
	if l.FileBytes == 0 {
		l.FileBytes = 5 << 20
	}
	if l.Entries == 0 {
		l.Entries = 5000
	}
	// N+1 must remain representable by io.LimitReader.
	if l.FileBytes == 1<<63-1 {
		return l, errors.New("invalid package file limit")
	}
	return l, nil
}

type packageContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r packageContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.r.Read(p)
	if canceled := r.ctx.Err(); canceled != nil {
		return n, canceled
	}
	return n, err
}

// InspectPackageZIP inspects a bounded ZIP entirely in memory, without writing
// or executing anything. Each SKILL.md defines a separate candidate. Shared
// repository files are returned separately for explicit association/review.
func InspectPackageZIP(ctx context.Context, source io.ReaderAt, size int64) ([]PackageCandidate, []string, error) {
	return InspectPackageZIPWithLimits(ctx, source, size, PackageLimits{})
}

// InspectPackageZIPWithLimits applies explicit host limits. Cancellation is
// checked during decompression; an arbitrary blocking ReaderAt itself must be
// given a deadline by its owner (this function cannot interrupt its I/O).
func InspectPackageZIPWithLimits(ctx context.Context, source io.ReaderAt, size int64, limits PackageLimits) ([]PackageCandidate, []string, error) {
	return inspectPackageZIPWithCapture(ctx, source, size, limits, nil)
}

// ReadPackageZIPFiles reuses the P1 framing, confinement, CRC and budget gates
// for containers such as agent packs; it does not require a root SKILL.md.
func ReadPackageZIPFiles(ctx context.Context, source io.ReaderAt, size int64, limits PackageLimits) ([]PackageFile, error) {
	var files []PackageFile
	_, _, err := inspectPackageZIPWithCapture(ctx, source, size, limits, func(v []PackageFile) { files = v })
	return files, err
}
func inspectPackageZIPWithCapture(ctx context.Context, source io.ReaderAt, size int64, limits PackageLimits, capture func([]PackageFile)) ([]PackageCandidate, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, nil, err
	}
	if size < 0 || size > limits.CompressedBytes {
		return nil, nil, errors.New("compressed package limit exceeded")
	}
	if err := preflightPackageZIP(ctx, source, size, limits); err != nil {
		return nil, nil, err
	}
	r, err := zip.NewReader(source, size)
	if err != nil {
		return nil, nil, err
	}
	if len(r.File) > limits.Entries {
		return nil, nil, errors.New("package entry limit exceeded")
	}
	var files []PackageFile
	var total int64
	inventory := make(packageInventory)
	for _, entry := range r.File {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		name := entry.Name
		if entry.FileInfo().IsDir() {
			name = strings.TrimSuffix(name, "/")
		}
		if err := inventory.add(name, entry.FileInfo().IsDir()); err != nil {
			return nil, nil, err
		}
		if entry.FileInfo().IsDir() {
			continue
		}
		if !entry.Mode().IsRegular() {
			return nil, nil, errors.New("non-regular package entry")
		}
		limit := limits.FileBytes
		if path.Base(name) == "SKILL.md" && limit > maxSkillMarkdown {
			limit = maxSkillMarkdown
		}
		if remaining := limits.ExpandedBytes - total; limit > remaining {
			limit = remaining
		}
		if entry.UncompressedSize64 > uint64(limit) {
			return nil, nil, errors.New("package file limit exceeded")
		}
		rc, err := entry.Open()
		if err != nil {
			return nil, nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(packageContextReader{ctx, rc}, limit+1))
		closeErr := rc.Close()
		if readErr != nil {
			return nil, nil, readErr
		}
		if closeErr != nil {
			return nil, nil, closeErr
		}
		if int64(len(body)) > limit {
			return nil, nil, errors.New("package file limit exceeded")
		}
		total += int64(len(body))
		if total > limits.ExpandedBytes {
			return nil, nil, errors.New("expanded package limit exceeded")
		}
		files = append(files, PackageFile{Path: name, Content: body, Executable: entry.Mode().Perm()&0111 != 0})
	}
	if capture != nil {
		if _, err := PackageDigest(files); err != nil {
			return nil, nil, err
		}
		capture(files)
		return nil, nil, nil
	}
	return inspectPackageFiles(ctx, files)
}

// Both acquisition paths use the same parser and nested-package ownership.
func inspectPackageFiles(ctx context.Context, files []PackageFile) ([]PackageCandidate, []string, error) {
	if _, err := PackageDigest(files); err != nil {
		return nil, nil, err
	}
	var candidates []PackageCandidate
	for _, file := range files {
		if path.Base(file.Path) != "SKILL.md" {
			continue
		}
		manifest, err := ParsePackageManifest(file.Content)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file.Path, err)
		}
		candidates = append(candidates, PackageCandidate{Subpath: path.Dir(file.Path), Manifest: manifest})
	}
	if len(candidates) == 0 {
		return nil, nil, errors.New("no SKILL.md candidates")
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Subpath < candidates[j].Subpath })
	var shared []string
	sharedFiles := make(map[string]PackageFile)
	for _, file := range files {
		owner := -1
		for i, c := range candidates {
			if c.Subpath == "." || strings.HasPrefix(file.Path, c.Subpath+"/") {
				if owner < 0 || len(c.Subpath) > len(candidates[owner].Subpath) {
					owner = i
				}
			}
		}
		if owner < 0 {
			shared = append(shared, file.Path)
			sharedFiles[file.Path] = file
			continue
		}
		copy := file
		if candidates[owner].Subpath != "." {
			copy.Path = strings.TrimPrefix(file.Path, candidates[owner].Subpath+"/")
		}
		candidates[owner].files = append(candidates[owner].files, copy)
	}
	for i := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		digest, err := PackageDigest(candidates[i].files)
		if err != nil {
			return nil, nil, err
		}
		candidates[i].Digest = digest
		candidates[i].shared = sharedFiles
	}
	sort.Strings(shared)
	return candidates, shared, nil
}
