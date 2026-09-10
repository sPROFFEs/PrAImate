package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"git.jtsec.local/lab/PrAImate/internal/skills"
)

// SkillLibraryRequest is host UI input, never model-tool or package metadata.
// Mutations use a host revision and publication/import uses a content review
// digest. Neither installation nor publication grants approval or selection.
type SkillLibraryRequest struct {
	DraftRevision uint64                  `json:"draft_revision,omitempty"`
	Action        string                  `json:"action"`
	Revision      uint64                  `json:"revision,omitempty"`
	Ref           string                  `json:"ref,omitempty"`
	Digest        string                  `json:"digest,omitempty"`
	Review        string                  `json:"review,omitempty"`
	Key           string                  `json:"key,omitempty"`
	NewRef        string                  `json:"new_ref,omitempty"`
	Files         []skills.PackageFile    `json:"files,omitempty"`
	Source        string                  `json:"source,omitempty"`
	Kind          string                  `json:"kind,omitempty"`
	GitRef        string                  `json:"git_ref,omitempty"`
	Subpath       string                  `json:"subpath,omitempty"`
	Selections    []SkillLibrarySelection `json:"selections,omitempty"`
	Approved      bool                    `json:"approved,omitempty"`
}

type SkillLibrarySelection struct {
	Index  int                        `json:"index"`
	Ref    string                     `json:"ref"`
	Shared []skills.SharedAssociation `json:"shared,omitempty"`
}

type SkillLibraryPackage struct {
	Index      int                     `json:"index"`
	Subpath    string                  `json:"subpath"`
	Ref        string                  `json:"ref,omitempty"`
	SourceID   string                  `json:"source_id,omitempty"`
	Digest     string                  `json:"digest"`
	Manifest   skills.PackageManifest  `json:"manifest"`
	Files      []skills.PackageFile    `json:"files"`
	Provenance skills.SourceProvenance `json:"provenance,omitempty"`
}

type SkillLibraryResult struct {
	Legacy         []skills.LegacyMigrationEntry   `json:"legacy,omitempty"`
	LegacyPackages map[string][]skills.PackageFile `json:"legacy_packages,omitempty"`
	View           *skills.SkillHostView           `json:"view,omitempty"`
	Packages       []SkillLibraryPackage           `json:"packages,omitempty"`
	Shared         []string                        `json:"shared,omitempty"`
	Review         string                          `json:"review,omitempty"`
	GitRef         string                          `json:"git_ref,omitempty"`
	Version        *skills.SkillVersion            `json:"version,omitempty"`
	Files          []skills.PackageFile            `json:"files,omitempty"`
	Approved       bool                            `json:"approved,omitempty"`
	DraftRevision  uint64                          `json:"draft_revision,omitempty"`
	Key            string                          `json:"key,omitempty"`
	Changes        *skills.SkillUpdatePreview      `json:"changes,omitempty"`
	Summaries      []InstalledSkillSummary         `json:"summaries,omitempty"`
}

func skillReview(value any) string {
	body, _ := json.Marshal(value)
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// The interactive editor deliberately has a lower aggregate memory limit than
// bulk pack import; excess content is rejected rather than truncated.
func libraryLimits() skills.PackageLimits {
	return skills.PackageLimits{ExpandedBytes: 8 << 20, Entries: 1000}
}

func inspectLibrarySource(ctx context.Context, in SkillLibraryRequest) (SkillLibraryResult, []skills.SelectedPackage, error) {
	var out SkillLibraryResult
	var candidates []skills.PackageCandidate
	var err error
	var provenance skills.SourceProvenance
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	switch in.Kind {
	case "directory":
		candidates, out.Shared, err = skills.InspectPackageDirectory(ctx, in.Source, libraryLimits())
	case "zip":
		var info os.FileInfo
		info, err = os.Lstat(in.Source)
		if err == nil && !info.Mode().IsRegular() {
			err = errors.New("ZIP source must be a regular file")
		}
		if err == nil {
			var f *os.File
			f, err = os.Open(in.Source)
			if err == nil {
				defer f.Close()
				opened, statErr := f.Stat()
				if statErr != nil || !os.SameFile(info, opened) {
					return out, nil, errors.New("ZIP source changed while opening")
				}
				candidates, out.Shared, err = skills.InspectPackageZIPWithLimits(ctx, f, info.Size(), libraryLimits())
			}
		}
	case "github":
		var inspected skills.GitPackageInspection
		inspected, err = skills.FetchGitHubPackages(ctx, skills.GitPackageSource{Repository: in.Source, Ref: in.GitRef, Subpath: in.Subpath}, skills.PackageNetworkPolicy{}, libraryLimits())
		if err == nil {
			candidates, out.Shared, out.GitRef = inspected.Candidates, inspected.Shared, inspected.ResolvedRevision
			provenance = skills.SourceProvenance{Kind: "external", Origin: inspected.Source.Repository, ResolvedRevision: inspected.ResolvedRevision}
		}
	default:
		return out, nil, errors.New("choose directory, zip or github source")
	}
	if err != nil {
		return out, nil, err
	}
	choices := in.Selections
	if len(choices) == 0 {
		for i := range candidates {
			choices = append(choices, SkillLibrarySelection{Index: i})
		}
	}
	var selections []skills.PackageSelection
	for _, choice := range choices {
		if choice.Index < 0 || choice.Index >= len(candidates) {
			return out, nil, errors.New("candidate index is outside the inspection")
		}
		selections = append(selections, skills.PackageSelection{Candidate: choice.Index, ExpectedDigest: candidates[choice.Index].Digest, Shared: choice.Shared})
	}
	selected, err := skills.SelectPackages(ctx, candidates, selections, libraryLimits())
	if err != nil {
		return out, nil, err
	}
	for i, choice := range choices {
		candidate := candidates[choice.Index]
		p := provenance
		p.Subpath = candidate.Subpath
		identityPath := filepath.Clean(in.Source) + "\x00" + candidate.Subpath
		sourceID := "local:" + strings.TrimPrefix(skillReview(identityPath), "sha256:")
		if p.Kind == "external" {
			sourceID, err = skills.ExternalSkillSourceID(p.Origin, p.Subpath)
			if err != nil {
				return out, nil, err
			}
		}
		out.Packages = append(out.Packages, SkillLibraryPackage{Index: choice.Index, Subpath: candidate.Subpath, Ref: choice.Ref, SourceID: sourceID, Digest: selected[i].Digest(), Manifest: candidate.Manifest, Files: selected[i].Files(), Provenance: p})
	}
	out.Review = skillReview(out.Packages)
	return out, selected, nil
}

// SkillLibrary is shared host functionality. Keep it out of runtime tool maps
// and detached-session RPC: those scopes may select, not edit host authority.
func (c *Core) SkillLibrary(ctx context.Context, in SkillLibraryRequest) (SkillLibraryResult, error) {
	var out SkillLibraryResult
	if err := c.requireSkillsV2Rollout(ctx); err != nil {
		return out, err
	}
	host, err := OpenSkillStore(skills.PackageLimits{})
	if err != nil {
		return out, err
	}
	defer host.Close()
	view, err := host.View(ctx)
	if err != nil {
		return out, err
	}
	if in.Action == "list" {
		out.View = &view
		out.Summaries, err = installedSkillSummaries(ctx, host, view.Versions)
		return out, nil
	}
	if in.Action == "legacy-inspect" || in.Action == "legacy-migrate" {
		plan, err := PreviewStoredLegacySkills(ctx, libraryLimits())
		if err != nil {
			return out, err
		}
		out.Legacy, out.LegacyPackages, out.Review = plan.Entries(), plan.ReviewPackages(), plan.ReviewDigest()
		if in.Action == "legacy-inspect" {
			return out, nil
		}
		if in.Review == "" || in.Review != out.Review {
			return out, errors.New("legacy catalogue changed; review again")
		}
		_, err = host.Update(ctx, in.Revision, func(tx *skills.SkillHostTransaction) error {
			_, err := tx.MigrateLegacy(ctx, plan, false)
			return err
		})
		return out, err
	}
	if in.Action == "inspect" || in.Action == "install" {
		preview, selected, err := inspectLibrarySource(ctx, in)
		if err != nil {
			return out, err
		}
		if in.Action == "inspect" {
			return preview, nil
		}
		if len(in.Selections) == 0 || in.Review == "" || in.Review != preview.Review {
			return out, errors.New("review changed or no selection; inspect the exact selection again")
		}
		_, err = host.Update(ctx, in.Revision, func(tx *skills.SkillHostTransaction) error {
			for i, pkg := range preview.Packages {
				if _, err := tx.RegisterSource(ctx, pkg.SourceID, pkg.Ref, selected[i], pkg.Provenance); err != nil {
					return err
				}
			}
			return nil
		})
		return preview, err
	}
	if in.Action == "read" {
		v, files, err := host.ReadVersion(ctx, in.Ref, in.Digest)
		if err != nil {
			return out, err
		}
		out.Version, out.Files = &v, files
		// Approval is observed in a read-only transaction below via SnapshotVersion.
		out.Approved, err = host.VersionApproved(ctx, in.Ref, in.Digest)
		return out, err
	}
	if in.Action == "draft-read" || in.Action == "draft-preview" {
		draft, err := host.ReadDraft(ctx, in.Key)
		if err != nil {
			return out, err
		}
		out.DraftRevision, out.Files = draft.Snapshot()
		out.Key = in.Key
		if in.Action == "draft-preview" {
			selected, err := draft.Preview(ctx, out.DraftRevision)
			if err != nil {
				return out, err
			}
			out.Review = selected.Digest()
			if in.Ref != "" && in.Digest != "" {
				diff, err := host.PreviewUpdate(ctx, in.Ref, in.Digest, selected)
				if err != nil {
					return out, err
				}
				out.Changes = &diff
			}
		}
		return out, nil
	}
	_, err = host.Update(ctx, in.Revision, func(tx *skills.SkillHostTransaction) error {
		switch in.Action {
		case "approve":
			// Exact installed content must have been reviewed, not merely its name.
			if in.Approved && in.Review != in.Digest {
				return errors.New("review the exact installed digest before approving")
			}
			return tx.Approve(ctx, in.Ref, in.Digest, in.Approved)
		case "forget":
			return tx.Forget(in.Ref, in.Digest)
		case "draft-create", "fork", "edit":
			var draft *skills.SkillDraft
			var err error
			source, err := skills.NewOwnSkillSourceID()
			if err != nil {
				return err
			}
			if in.Action == "fork" {
				draft, err = tx.Fork(ctx, in.Ref, in.Digest, in.NewRef)
			} else if in.Action == "edit" {
				draft, err = tx.EditOwnVersion(ctx, in.Ref, in.Digest)
			} else {
				draft, err = skills.NewSkillDraft(source, in.NewRef, in.Files, libraryLimits())
			}
			if err != nil {
				return err
			}
			out.Key = source
			return tx.SaveDraft(ctx, out.Key, draft)
		case "draft-save", "publish":
			draft, err := tx.LoadDraft(ctx, in.Key)
			if err != nil {
				return err
			}
			revision, _ := draft.Snapshot()
			if in.Action == "draft-save" {
				if _, err := draft.ReplaceFiles(in.DraftRevision, in.Files); err != nil {
					return err
				}
				return tx.SaveDraft(ctx, in.Key, draft)
			}
			selected, err := draft.Preview(ctx, revision)
			if err != nil {
				return err
			}
			if in.Review == "" || in.Review != selected.Digest() {
				return errors.New("draft changed; preview again before publishing")
			}
			v, err := tx.Publish(ctx, draft, revision)
			out.Version = &v
			return err
		default:
			return errors.New("unknown skill library action")
		}
	})
	return out, err
}
