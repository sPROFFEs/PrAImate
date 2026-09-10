package skills

import (
	"context"
	"errors"
	"sync"
)

// SkillDraft is editable authoring state, not a published version. Revision
// checks prevent stale editor saves and publishing a different preview. Drafts
// may temporarily contain invalid Markdown; publication validates the package.
type SkillDraft struct {
	mu         sync.RWMutex
	sourceID   string
	ref        string
	revision   uint64
	files      []PackageFile
	limits     PackageLimits
	provenance SourceProvenance
}

func NewSkillDraft(sourceID, ref string, files []PackageFile, limits PackageLimits) (*SkillDraft, error) {
	if err := validateRegistryIdentity(sourceID, ref); err != nil {
		return nil, err
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if err := validateDraftFiles(files, limits); err != nil {
		return nil, err
	}
	return &SkillDraft{sourceID: sourceID, ref: ref, files: clonePackageFiles(files), revision: 1, limits: limits, provenance: SourceProvenance{Kind: "own"}}, nil
}

func validateDraftFiles(files []PackageFile, limits PackageLimits) error {
	if len(files) > limits.Entries {
		return errors.New("draft entry limit exceeded")
	}
	var total int64
	for _, file := range files {
		if int64(len(file.Content)) > limits.FileBytes || int64(len(file.Content)) > limits.ExpandedBytes-total {
			return errors.New("draft byte limit exceeded")
		}
		total += int64(len(file.Content))
	}
	_, err := PackageDigest(files)
	return err
}

func (d *SkillDraft) Snapshot() (uint64, []PackageFile) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.revision, clonePackageFiles(d.files)
}

func (d *SkillDraft) ReplaceFiles(expectedRevision uint64, files []PackageFile) (uint64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if expectedRevision != d.revision {
		return d.revision, errors.New("draft revision conflict")
	}
	if d.revision == ^uint64(0) {
		return d.revision, errors.New("draft revision exhausted")
	}
	if err := validateDraftFiles(files, d.limits); err != nil {
		return d.revision, err
	}
	d.files = clonePackageFiles(files)
	d.revision++
	return d.revision, nil
}

func (d *SkillDraft) preview(ctx context.Context, expectedRevision uint64) (SelectedPackage, error) {
	if expectedRevision != d.revision {
		return SelectedPackage{}, errors.New("draft revision conflict")
	}
	candidates, shared, err := inspectPackageFiles(ctx, d.files)
	if err != nil {
		return SelectedPackage{}, err
	}
	if len(candidates) != 1 || candidates[0].Subpath != "." || len(shared) != 0 {
		return SelectedPackage{}, errors.New("draft requires exactly one root SKILL.md")
	}
	selected, err := SelectPackages(ctx, candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, d.limits)
	if err != nil {
		return SelectedPackage{}, err
	}
	return selected[0], nil
}

func (d *SkillDraft) Preview(ctx context.Context, expectedRevision uint64) (SelectedPackage, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.preview(ctx, expectedRevision)
}

// Publish serializes editor changes for the duration of validating/staging one
// revision. Content is installed before registration; failed registration can
// leave an unreferenced immutable object, never a dangling registry reference.
// The caller saves a catalogue checkpoint separately; this does not enable it.
func (d *SkillDraft) Publish(ctx context.Context, catalogue *VersionCatalogue, expectedRevision uint64) (SkillVersion, error) {
	if catalogue == nil {
		return SkillVersion{}, errors.New("catalogue required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	selected, err := d.preview(ctx, expectedRevision)
	if err != nil {
		return SkillVersion{}, err
	}
	generation, err := InstallPackagesWithLimits(ctx, catalogue.store, []SelectedPackage{selected}, catalogue.limits)
	if err != nil {
		return SkillVersion{}, err
	}
	return catalogue.RegisterWithProvenance(ctx, d.sourceID, d.ref, generation, selected.Digest(), d.provenance)
}
