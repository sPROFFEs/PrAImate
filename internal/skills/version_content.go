package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"sort"
)

// versionFiles verifies the entire generation, then rehashes the returned copy.
// No caller receives a mutable path into the object store.
func (c *VersionCatalogue) versionFiles(ctx context.Context, ref, digest string) (SkillVersion, []PackageFile, error) {
	v, err := c.Resolve(ref, digest)
	if err != nil {
		return v, nil, err
	}
	if _, err := VerifyPackageInstallation(ctx, c.store, v.Generation, c.limits); err != nil {
		return v, nil, err
	}
	read := func(name string, limit int64) ([]byte, error) {
		info, err := c.store.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("non-regular version resource")
		}
		body, _, err := readLocalPackageFile(ctx, c.store, name, info, limit)
		return body, err
	}
	prefix := v.Generation + "/objects/" + digest[7:]
	body, err := read(prefix+".json", 1<<20)
	if err != nil {
		return v, nil, err
	}
	var inventory []struct {
		Path       string `json:"path"`
		Executable bool   `json:"executable,omitempty"`
	}
	if err := decodePackageRecord(body, &inventory); err != nil {
		return v, nil, err
	}
	if len(inventory) > c.limits.Entries {
		return v, nil, errors.New("version entry limit exceeded")
	}
	files := make([]PackageFile, 0, len(inventory))
	var total int64
	for _, item := range inventory {
		if err := validatePackagePath(item.Path); err != nil {
			return v, nil, err
		}
		limit := min(c.limits.FileBytes, c.limits.ExpandedBytes-total)
		content, err := read(prefix+"/"+item.Path, limit)
		if err != nil {
			return v, nil, err
		}
		total += int64(len(content))
		files = append(files, PackageFile{Path: item.Path, Content: content, Executable: item.Executable})
	}
	actual, err := PackageDigest(files)
	if err != nil {
		return v, nil, err
	}
	if actual != digest {
		return v, nil, errors.New("version changed while reading")
	}
	return v, files, nil
}

func (s *HostSkillStore) ReadVersion(ctx context.Context, ref, digest string) (SkillVersion, []PackageFile, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return SkillVersion{}, nil, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return SkillVersion{}, nil, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return SkillVersion{}, nil, err
	}
	defer func() { tx.active = false }()
	return tx.catalogue.versionFiles(ctx, ref, digest)
}

// FileChange is a bounded exact-byte side-by-side diff, including scripts and
// executable intent. Nil denotes absence, not an empty file. No LLM rewrites.
type FileChange struct {
	Path          string
	Before, After *PackageFile
}
type SkillUpdatePreview struct {
	Previous SkillVersion
	Digest   string
	Changes  []FileChange
}

func (s *HostSkillStore) PreviewUpdate(ctx context.Context, ref, digest string, selected SelectedPackage) (SkillUpdatePreview, error) {
	v, old, err := s.ReadVersion(ctx, ref, digest)
	if err != nil {
		return SkillUpdatePreview{}, err
	}
	if err := validateDraftFiles(selected.files, s.limits); err != nil {
		return SkillUpdatePreview{}, err
	}
	actual, err := PackageDigest(selected.files)
	if err != nil || actual != selected.digest {
		return SkillUpdatePreview{}, errors.New("invalid update selection")
	}
	before := make(map[string]PackageFile)
	after := make(map[string]PackageFile)
	names := make(map[string]bool)
	for _, f := range old {
		before[f.Path] = f
		names[f.Path] = true
	}
	for _, f := range selected.Files() {
		after[f.Path] = f
		names[f.Path] = true
	}
	preview := SkillUpdatePreview{Previous: v, Digest: actual}
	for name := range names {
		if err := ctx.Err(); err != nil {
			return SkillUpdatePreview{}, err
		}
		a, existsA := before[name]
		b, existsB := after[name]
		if existsA && existsB && a.Executable == b.Executable && bytes.Equal(a.Content, b.Content) {
			continue
		}
		change := FileChange{Path: name}
		if existsA {
			change.Before = &a
		}
		if existsB {
			change.After = &b
		}
		preview.Changes = append(preview.Changes, change)
	}
	sort.Slice(preview.Changes, func(i, j int) bool { return preview.Changes[i].Path < preview.Changes[j].Path })
	return preview, nil
}

// EditOwnVersion starts an independent draft; published content stays immutable.
// Imported and built-in procedures require an explicitly named fork.
func (tx *SkillHostTransaction) EditOwnVersion(ctx context.Context, ref, digest string) (*SkillDraft, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	v, files, err := tx.catalogue.versionFiles(ctx, ref, digest)
	if err != nil {
		return nil, err
	}
	if v.SourceKind != "own" {
		return nil, errors.New("use an explicit fork to edit imported or built-in skills")
	}
	draft, err := NewSkillDraft(v.SourceID, v.Ref, files, tx.store.limits)
	if err != nil {
		return nil, err
	}
	draft.provenance = v.Provenance()
	return draft, nil
}

// Fork creates a new own identity and preserves every resource/license verbatim.
// The parent digest is provenance, not inherited approval or executable rights.
func (tx *SkillHostTransaction) Fork(ctx context.Context, ref, digest, newRef string) (*SkillDraft, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	parent, files, err := tx.catalogue.versionFiles(ctx, ref, digest)
	if err != nil {
		return nil, err
	}
	source, err := NewOwnSkillSourceID()
	if err != nil {
		return nil, err
	}
	draft, err := NewSkillDraft(source, newRef, files, tx.store.limits)
	if err != nil {
		return nil, err
	}
	draft.provenance = SourceProvenance{Kind: "own", DerivedSource: parent.SourceID, DerivedDigest: parent.Digest}
	return draft, nil
}

// ExportPackageZIP exports only reviewed package bytes. It never walks the host
// directory or serializes approvals, leases, sessions, backups or credentials.
// User-authored secrets inside package resources are not automatically redacted:
// callers must review ReadVersion before sharing. Agent packs belong to P3.
func (s *HostSkillStore) ExportPackageZIP(ctx context.Context, ref, digest string) ([]byte, error) {
	_, files, err := s.ReadVersion(ctx, ref, digest)
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var output bytes.Buffer
	w := zip.NewWriter(&output)
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		header := &zip.FileHeader{Name: f.Path, Method: zip.Deflate}
		mode := os.FileMode(0644)
		if f.Executable {
			mode = 0755
		}
		header.SetMode(mode)
		entry, err := w.CreateHeader(header)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write(f.Content); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	if int64(output.Len()) > s.limits.CompressedBytes {
		return nil, errors.New("export exceeds compressed package limit")
	}
	return output.Bytes(), nil
}
