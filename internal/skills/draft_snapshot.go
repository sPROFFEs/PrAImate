package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Authoring checkpoints are bounded separately from installed packages. A
// larger valid bundle may still require a smaller draft or a future file-backed
// editor; never truncate an oversized draft silently.
const maxDraftCheckpoint = 16 << 20

type draftCheckpoint struct {
	Schema     string                `json:"schema"`
	SourceID   string                `json:"source_id"`
	Ref        string                `json:"ref"`
	Revision   uint64                `json:"revision"`
	Files      []draftCheckpointFile `json:"files"`
	Provenance SourceProvenance      `json:"provenance,omitempty"`
}
type draftCheckpointFile struct {
	Path       string `json:"path"`
	Content    []byte `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

func (d *SkillDraft) SaveCheckpoint(ctx context.Context, store *os.Root) (string, error) {
	if store == nil {
		return "", errors.New("draft store required")
	}
	d.mu.RLock()
	record := draftCheckpoint{Schema: "praimate.skill-draft/v1", SourceID: d.sourceID, Ref: d.ref, Revision: d.revision, Files: make([]draftCheckpointFile, len(d.files))}
	record.Provenance = d.provenance
	var size int64
	for i, file := range d.files {
		size += int64(len(file.Content))
		if size > maxDraftCheckpoint/2 {
			d.mu.RUnlock()
			return "", errors.New("draft checkpoint content limit exceeded")
		}
		record.Files[i] = draftCheckpointFile{file.Path, file.Content, file.Executable}
	}
	body, err := json.Marshal(record)
	d.mu.RUnlock()
	if err != nil {
		return "", err
	}
	if len(body) > maxDraftCheckpoint {
		return "", errors.New("draft checkpoint limit exceeded")
	}
	return saveImmutableRecord(ctx, store, "draft", body)
}

// saveImmutableRecord does not update a mutable head pointer. The host settings
// transaction chooses the resulting ID; older checkpoints remain rollback data.
func saveImmutableRecord(ctx context.Context, store *os.Root, kind string, body []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	hash := sha256.Sum256(body)
	name := kind + "-" + hex.EncodeToString(hash[:]) + ".json"
	if info, err := store.Lstat(name); err == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("non-regular checkpoint")
		}
		existing, _, err := readLocalPackageFile(ctx, store, name, info, int64(len(body)))
		if err != nil {
			return "", err
		}
		if string(existing) != string(body) {
			return "", errors.New("checkpoint integrity mismatch")
		}
		return name, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	tmp := ".record-stage-" + hex.EncodeToString(nonce[:])
	defer store.Remove(tmp)
	if err := writePackageObject(store, tmp, body); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := store.Rename(tmp, name); err != nil {
		return "", err
	}
	if err := syncPackageDirectory(store, "."); err != nil {
		return name, err
	}
	return name, nil
}

func LoadSkillDraft(ctx context.Context, store *os.Root, name string, limits PackageLimits) (*SkillDraft, error) {
	if store == nil {
		return nil, errors.New("draft store required")
	}
	if len(name) != 75 || !strings.HasPrefix(name, "draft-") || !strings.HasSuffix(name, ".json") {
		return nil, errors.New("invalid draft checkpoint ID")
	}
	info, err := store.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("non-regular draft checkpoint")
	}
	body, _, err := readLocalPackageFile(ctx, store, name, info, maxDraftCheckpoint)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	if name != "draft-"+hex.EncodeToString(hash[:])+".json" {
		return nil, errors.New("draft integrity mismatch")
	}
	var record draftCheckpoint
	if err := decodePackageRecord(body, &record); err != nil {
		return nil, err
	}
	if record.Schema != "praimate.skill-draft/v1" || record.Revision == 0 {
		return nil, errors.New("invalid draft checkpoint")
	}
	files := make([]PackageFile, len(record.Files))
	for i, file := range record.Files {
		files[i] = PackageFile{Path: file.Path, Content: file.Content, Executable: file.Executable}
	}
	draft, err := NewSkillDraft(record.SourceID, record.Ref, files, limits)
	if err != nil {
		return nil, err
	}
	draft.revision = record.Revision
	if err := record.Provenance.Validate(); err != nil {
		return nil, err
	}
	if record.Provenance.DerivedSource == record.SourceID {
		return nil, errors.New("fork requires a distinct source identity")
	}
	if record.Provenance.Kind != "" {
		draft.provenance = record.Provenance
	}
	return draft, nil
}
