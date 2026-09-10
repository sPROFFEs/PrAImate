package skills

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// SkillVersion is content identity, not permission or activation. Versions
// returned to callers are value snapshots; publishing another digest cannot
// mutate a version already pinned by a caller.
type SkillVersion struct {
	SourceID         string `json:"source_id,omitempty"`
	Ref              string `json:"ref,omitempty"`
	Digest           string `json:"digest,omitempty"`
	Generation       string `json:"generation,omitempty"`
	SourceKind       string `json:"source_kind,omitempty"`
	Origin           string `json:"origin,omitempty"`
	Subpath          string `json:"subpath,omitempty"`
	ResolvedRevision string `json:"resolved_revision,omitempty"`
	DerivedSource    string `json:"derived_source,omitempty"`
	DerivedDigest    string `json:"derived_digest,omitempty"`
}

// VersionCatalogue is the in-memory registry model. Persistence, leases and
// migration are layered on this model in P2; it is not yet a global catalogue.
// The host owns store lifetime and source IDs. Imported manifests never become
// registry records and cannot grant permissions through this API.
type VersionCatalogue struct {
	mu       sync.RWMutex
	store    *os.Root
	limits   PackageLimits
	refs     map[string]string
	versions map[string]map[string]SkillVersion
}

func NewVersionCatalogue(store *os.Root, limits PackageLimits) (*VersionCatalogue, error) {
	if store == nil {
		return nil, errors.New("package store is required")
	}
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	return &VersionCatalogue{store: store, limits: limits, refs: make(map[string]string), versions: make(map[string]map[string]SkillVersion)}, nil
}

func validateRegistryIdentity(sourceID, ref string) error {
	if strings.TrimSpace(sourceID) == "" || len(sourceID) > 512 || !utf8.ValidString(sourceID) {
		return errors.New("invalid source identity")
	}
	for _, r := range sourceID {
		if unicode.IsControl(r) {
			return errors.New("invalid source identity")
		}
	}
	if len(ref) > 255 || strings.Count(ref, "/") != 1 {
		return errors.New("skill ref requires namespace/name")
	}
	if err := validatePackagePath(ref); err != nil {
		return err
	}
	return nil
}

// Register adds an immutable version only after verifying the whole installed
// generation. Name collisions never imply override. Repeated registration of
// the same source/digest/ref is idempotent; alias rebinding is a separate action.
func (c *VersionCatalogue) Register(ctx context.Context, sourceID, ref, generation, digest string) (SkillVersion, error) {
	return c.RegisterWithProvenance(ctx, sourceID, ref, generation, digest, SourceProvenance{})
}

func (c *VersionCatalogue) RegisterWithProvenance(ctx context.Context, sourceID, ref, generation, digest string, provenance SourceProvenance) (SkillVersion, error) {
	if err := provenance.Validate(); err != nil {
		return SkillVersion{}, err
	}
	if provenance.DerivedSource == sourceID {
		return SkillVersion{}, errors.New("fork requires a distinct source identity")
	}
	if err := validateRegistryIdentity(sourceID, ref); err != nil {
		return SkillVersion{}, err
	}
	receipt, err := VerifyPackageInstallation(ctx, c.store, generation, c.limits)
	if err != nil {
		return SkillVersion{}, err
	}
	found := false
	for _, installed := range receipt.Digests {
		if installed == digest {
			found = true
			break
		}
	}
	if !found {
		return SkillVersion{}, errors.New("digest not present in installed generation")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return SkillVersion{}, err
	}
	key := packagePathKey(ref)
	if owner, exists := c.refs[key]; exists && owner != sourceID {
		return SkillVersion{}, errors.New("skill ref already belongs to another source")
	}
	for _, version := range c.versions[sourceID] {
		if version.Ref != ref {
			return SkillVersion{}, errors.New("source already registered under another ref")
		}
		if !sameSourceProvenance(version.Provenance(), provenance) {
			return SkillVersion{}, errors.New("source provenance cannot be rebound; create a fork")
		}
	}
	if previous, exists := c.versions[sourceID][digest]; exists {
		if !sameSourceProvenance(previous.Provenance(), provenance) {
			return SkillVersion{}, errors.New("immutable version provenance conflict")
		}
		return previous, nil
	}
	version := SkillVersion{SourceID: sourceID, Ref: ref, Digest: digest, Generation: generation}
	version.SourceKind = provenance.Kind
	version.Origin = provenance.Origin
	version.Subpath = provenance.Subpath
	version.ResolvedRevision = provenance.ResolvedRevision
	version.DerivedSource = provenance.DerivedSource
	version.DerivedDigest = provenance.DerivedDigest
	if c.versions[sourceID] == nil {
		c.versions[sourceID] = make(map[string]SkillVersion)
	}
	c.versions[sourceID][digest] = version
	c.refs[key] = sourceID
	return version, nil
}

// Resolve always requires a digest; there is no implicit latest-version upgrade.
func (c *VersionCatalogue) Resolve(ref, digest string) (SkillVersion, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	source, exists := c.refs[packagePathKey(ref)]
	if !exists {
		return SkillVersion{}, errors.New("skill ref not found")
	}
	version, exists := c.versions[source][digest]
	if !exists {
		return SkillVersion{}, errors.New("skill version not found")
	}
	return version, nil
}

func (c *VersionCatalogue) Versions(ref string) []SkillVersion {
	c.mu.RLock()
	defer c.mu.RUnlock()
	source, exists := c.refs[packagePathKey(ref)]
	if !exists {
		return nil
	}
	versions := make([]SkillVersion, 0, len(c.versions[source]))
	for _, version := range c.versions[source] {
		versions = append(versions, version)
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Digest < versions[j].Digest })
	return versions
}
