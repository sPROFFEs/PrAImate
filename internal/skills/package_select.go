package skills

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
)

// SharedAssociation explicitly copies an inspected repository resource into a
// selected bundle. This is a content change, not an inferred license grant.
type SharedAssociation struct {
	Source      string
	Destination string
}

type PackageSelection struct {
	Candidate      int
	ExpectedDigest string
	Shared         []SharedAssociation
}

// SelectedPackage retains private bytes for later staging. Callers receive
// only copies through Files; changing a preview cannot mutate the plan.
type SelectedPackage struct {
	digest string
	files  []PackageFile
}

func (p SelectedPackage) Digest() string { return p.digest }

func (p SelectedPackage) Files() []PackageFile {
	return clonePackageFiles(p.files)
}

func clonePackageFiles(files []PackageFile) []PackageFile {
	cloned := make([]PackageFile, len(files))
	for i, file := range files {
		cloned[i] = file
		cloned[i].Content = append([]byte(nil), file.Content...)
	}
	return cloned
}

// SelectPackages validates the whole selection before returning a plan. It
// never rereads the source or writes to disk. Limits apply across the selected
// set, including duplication of a shared resource into multiple bundles.
func SelectPackages(ctx context.Context, candidates []PackageCandidate, selections []PackageSelection, limits PackageLimits) ([]SelectedPackage, error) {
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selected := make([]SelectedPackage, 0, len(selections))
	seen := make(map[int]bool)
	var total int64
	entries := 0
	for _, selection := range selections {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if selection.Candidate < 0 || selection.Candidate >= len(candidates) || seen[selection.Candidate] {
			return nil, errors.New("invalid or duplicate package selection")
		}
		seen[selection.Candidate] = true
		candidate := candidates[selection.Candidate]
		digest, err := PackageDigest(candidate.files)
		if err != nil {
			return nil, err
		}
		if len(candidate.files) == 0 || digest != selection.ExpectedDigest || digest != candidate.Digest {
			return nil, errors.New("package preview integrity mismatch")
		}
		files := append([]PackageFile(nil), candidate.files...)
		for _, association := range selection.Shared {
			file, exists := candidate.shared[association.Source]
			if !exists {
				return nil, fmt.Errorf("shared resource not in inspected snapshot: %q", association.Source)
			}
			if err := validatePackagePath(association.Destination); err != nil {
				return nil, err
			}
			if packagePathKey(path.Base(association.Destination)) == packagePathKey("SKILL.md") {
				return nil, errors.New("shared resource cannot introduce an entrypoint")
			}
			file.Path = association.Destination
			files = append(files, file)
		}
		for _, file := range files {
			entries++
			if entries > limits.Entries {
				return nil, errors.New("selected package entry limit exceeded")
			}
			if int64(len(file.Content)) > limits.FileBytes || int64(len(file.Content)) > limits.ExpandedBytes-total {
				return nil, errors.New("selected package byte limit exceeded")
			}
			total += int64(len(file.Content))
		}
		digest, err = PackageDigest(files)
		if err != nil {
			return nil, err
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		selected = append(selected, SelectedPackage{digest: digest, files: clonePackageFiles(files)})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return selected, nil
}
