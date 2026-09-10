package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// LegacyInlineSkill is supplied by the core adapter. It contains no runtime
// modes: an old checkbox must not silently become a pinned v2 binding.
type LegacyInlineSkill struct {
	ID          string
	Name        string
	Description string
	Body        string
	CLIs        []string
	Builtin     bool
	Invalid     string
}

type LegacyMigrationEntry struct {
	LegacyID         string `json:"legacy_id,omitempty"`
	Builtin          bool   `json:"builtin,omitempty"`
	Ref              string `json:"ref,omitempty"`
	SourceID         string `json:"source_id,omitempty"`
	Diagnostic       string `json:"diagnostic,omitempty"`
	OverridesBuiltin bool   `json:"overrides_builtin,omitempty"`
}

type LegacyMigrationPlan struct {
	entries  []LegacyMigrationEntry
	packages map[int]SelectedPackage
	original []byte
}

func (p *LegacyMigrationPlan) Entries() []LegacyMigrationEntry {
	return append([]LegacyMigrationEntry(nil), p.entries...)
}

// ReviewPackages exposes copies for a host review, never portable approvals.
func (p *LegacyMigrationPlan) ReviewPackages() map[string][]PackageFile {
	out := make(map[string][]PackageFile)
	for i, pkg := range p.packages {
		out[p.entries[i].Ref] = pkg.Files()
	}
	return out
}

func (p *LegacyMigrationPlan) ReviewDigest() string {
	body, _ := json.Marshal(struct {
		Entries  []LegacyMigrationEntry
		Packages map[string][]PackageFile
		Original []byte
	}{p.entries, p.ReviewPackages(), p.original})
	hash := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(hash[:])
}

// ReadLegacySkillCatalogue uses the same confined, identity-checked reader as
// package imports. Reject links/devices before opening; Linux opens nonblocking
// so a concurrent replacement with a FIFO cannot hang the migration preview.
func ReadLegacySkillCatalogue(ctx context.Context, directory string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Lstat("skills.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("legacy catalogue must be a regular non-link file")
	}
	body, _, err := readLocalPackageFile(ctx, root, "skills.json", info, maxRegistrySnapshot)
	return body, err
}

// PrepareLegacyMigration preserves exact legacy body bytes; it never edits the
// source catalogue. Invalid entries remain visible in the returned diagnostics.
func PrepareLegacyMigration(ctx context.Context, entries []LegacyInlineSkill, original []byte, limits PackageLimits) (*LegacyMigrationPlan, error) {
	if len(original) > maxRegistrySnapshot || len(entries) > maxRegistryVersions {
		return nil, errors.New("legacy migration limit exceeded")
	}
	plan := &LegacyMigrationPlan{packages: make(map[int]SelectedPackage), original: append([]byte(nil), original...)}
	builtins := make(map[string]bool)
	for _, entry := range entries {
		if entry.Builtin {
			builtins[entry.ID] = true
		}
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		kind := "user"
		if entry.Builtin {
			kind = "builtin"
		}
		identity := sha256.Sum256([]byte("praimate-legacy-v1\x00" + kind + "\x00" + entry.ID))
		id := hex.EncodeToString(identity[:])
		out := LegacyMigrationEntry{LegacyID: entry.ID, Builtin: entry.Builtin, Ref: "legacy-" + kind + "/skill-" + id[:16], SourceID: "legacy:" + kind + ":" + id, OverridesBuiltin: !entry.Builtin && builtins[entry.ID]}
		key := kind + "\x00" + entry.ID
		if entry.Invalid != "" {
			out.Diagnostic = entry.Invalid
		} else if entry.ID == "" || strings.TrimSpace(entry.Body) == "" {
			out.Diagnostic = "legacy skill requires an ID and nonempty body"
		} else if seen[key] {
			out.Diagnostic = "duplicate legacy ID; first entry retained"
		}
		seen[key] = true
		if out.Diagnostic == "" {
			// Use generated portable name; preserve the display name in metadata.
			clis, _ := json.Marshal(entry.CLIs)
			header, err := yaml.Marshal(struct {
				Name        string            `yaml:"name"`
				Description string            `yaml:"description"`
				Metadata    map[string]string `yaml:"metadata"`
			}{"skill-" + id[:16], entry.Description, map[string]string{"legacy-name": entry.Name, "legacy-id": entry.ID, "legacy-clis": string(clis)}})
			if err != nil {
				return nil, err
			}
			body := append([]byte("---\n"), header...)
			body = append(body, []byte("---\n")...)
			body = append(body, []byte(entry.Body)...)
			candidates, _, err := inspectPackageFiles(ctx, []PackageFile{{Path: "SKILL.md", Content: body}})
			if err != nil {
				out.Diagnostic = "legacy skill cannot form a valid package"
			} else {
				selected, err := SelectPackages(ctx, candidates, []PackageSelection{{Candidate: 0, ExpectedDigest: candidates[0].Digest}}, limits)
				if err != nil {
					out.Diagnostic = "legacy skill exceeds package limits"
				} else {
					plan.packages[len(plan.entries)] = selected[0]
				}
			}
		}
		plan.entries = append(plan.entries, out)
	}
	return plan, nil
}

type LegacyMigrationResult struct {
	Schema            string                 `json:"schema"`
	OriginalSnapshot  string                 `json:"original_snapshot"`
	CatalogueSnapshot string                 `json:"catalogue_snapshot"`
	Entries           []LegacyMigrationEntry `json:"entries"`
}

// ApplyLegacyMigration is explicit, additive and idempotent. The original
// catalogue remains untouched; rollback chooses legacy instead of this snapshot.
// Diagnostics must be acknowledged explicitly to publish a partial migration.
func ApplyLegacyMigration(ctx context.Context, store *os.Root, plan *LegacyMigrationPlan, acceptDiagnostics bool, limits PackageLimits) (LegacyMigrationResult, error) {
	var result LegacyMigrationResult
	if store == nil || plan == nil {
		return result, errors.New("migration store and plan required")
	}
	for _, entry := range plan.entries {
		if entry.Diagnostic != "" && !acceptDiagnostics {
			return result, errors.New("migration has unresolved diagnostics")
		}
	}
	backup, err := saveImmutableRecord(ctx, store, "legacy-backup", plan.original)
	if err != nil {
		return result, err
	}
	catalogue, err := NewVersionCatalogue(store, limits)
	if err != nil {
		return result, err
	}
	for i, entry := range plan.entries {
		selected, valid := plan.packages[i]
		if !valid {
			continue
		}
		generation, err := InstallPackagesWithLimits(ctx, store, []SelectedPackage{selected}, limits)
		if err != nil {
			return result, err
		}
		kind := "legacy-user"
		if entry.Builtin {
			kind = "builtin"
		}
		if _, err := catalogue.RegisterWithProvenance(ctx, entry.SourceID, entry.Ref, generation, selected.Digest(), SourceProvenance{Kind: kind}); err != nil {
			return result, err
		}
	}
	snapshot, err := catalogue.SaveSnapshot(ctx)
	if err != nil {
		return result, err
	}
	result = LegacyMigrationResult{Schema: "praimate.legacy-skill-migration/v1", OriginalSnapshot: backup, CatalogueSnapshot: snapshot, Entries: plan.Entries()}
	body, err := json.Marshal(result)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	if _, err := saveImmutableRecord(ctx, store, "migration", body); err != nil {
		return LegacyMigrationResult{}, err
	}
	return result, nil
}
