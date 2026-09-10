package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
)

const maxRegistrySnapshot = 4 << 20
const maxRegistryVersions = 1000

type registrySnapshot struct {
	Schema   string         `json:"schema"`
	Versions []SkillVersion `json:"versions"`
}

func registrySnapshotName(body []byte) string {
	hash := sha256.Sum256(body)
	return "registry-" + hex.EncodeToString(hash[:]) + ".json"
}

// SaveSnapshot persists an immutable checkpoint. The host must explicitly
// persist the returned ID in its settings transaction; no implicit latest
// pointer is changed. Earlier snapshots remain available for rollback.
func (c *VersionCatalogue) SaveSnapshot(ctx context.Context) (string, error) {
	c.mu.RLock()
	snapshot := registrySnapshot{Schema: "praimate.skill-registry/v1", Versions: []SkillVersion{}}
	for _, versions := range c.versions {
		for _, version := range versions {
			snapshot.Versions = append(snapshot.Versions, version)
		}
	}
	c.mu.RUnlock()
	if len(snapshot.Versions) > maxRegistryVersions {
		return "", errors.New("registry version limit exceeded")
	}
	sort.Slice(snapshot.Versions, func(i, j int) bool {
		a, b := snapshot.Versions[i], snapshot.Versions[j]
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		return a.Digest < b.Digest
	})
	body, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	if len(body) > maxRegistrySnapshot {
		return "", errors.New("registry snapshot limit exceeded")
	}
	// Revalidate installed generations before producing a persistent reference.
	if _, err := restoreRegistrySnapshot(ctx, c.store, body, c.limits); err != nil {
		return "", err
	}
	name := registrySnapshotName(body)
	if _, err := c.store.Lstat(name); err == nil {
		if _, err := LoadVersionCatalogue(ctx, c.store, name, c.limits); err != nil {
			return "", err
		}
		return name, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	tmp := ".registry-stage-" + hex.EncodeToString(nonce[:])
	defer c.store.Remove(tmp)
	if err := writePackageObject(c.store, tmp, body); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := c.store.Rename(tmp, name); err != nil {
		return "", err
	}
	if err := syncPackageDirectory(c.store, "."); err != nil {
		return name, err
	}
	return name, nil
}

// LoadVersionCatalogue restores exactly one host-selected checkpoint. It does
// not accept a manifest/agent pack as registry state or grant trust/permissions.
func LoadVersionCatalogue(ctx context.Context, store *os.Root, name string, limits PackageLimits) (*VersionCatalogue, error) {
	if store == nil {
		return nil, errors.New("package store is required")
	}
	if len(name) != 78 || !strings.HasPrefix(name, "registry-") || !strings.HasSuffix(name, ".json") {
		return nil, errors.New("invalid registry snapshot ID")
	}
	if _, err := hex.DecodeString(name[9:73]); err != nil {
		return nil, errors.New("invalid registry snapshot ID")
	}
	info, err := store.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("non-regular registry snapshot")
	}
	body, _, err := readLocalPackageFile(ctx, store, name, info, maxRegistrySnapshot)
	if err != nil {
		return nil, err
	}
	if registrySnapshotName(body) != name {
		return nil, errors.New("registry snapshot integrity mismatch")
	}
	return restoreRegistrySnapshot(ctx, store, body, limits)
}

func restoreRegistrySnapshot(ctx context.Context, store *os.Root, body []byte, limits PackageLimits) (*VersionCatalogue, error) {
	if len(body) > maxRegistrySnapshot {
		return nil, errors.New("registry snapshot limit exceeded")
	}
	var snapshot registrySnapshot
	if err := decodePackageRecord(body, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Schema != "praimate.skill-registry/v1" || len(snapshot.Versions) > maxRegistryVersions {
		return nil, errors.New("invalid registry snapshot")
	}
	catalogue, err := NewVersionCatalogue(store, limits)
	if err != nil {
		return nil, err
	}
	verified := make(map[string]map[string]bool)
	for _, version := range snapshot.Versions {
		if version.DerivedSource == version.SourceID {
			return nil, errors.New("fork requires a distinct source identity")
		}
		if err := version.Provenance().Validate(); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := validateRegistryIdentity(version.SourceID, version.Ref); err != nil {
			return nil, err
		}
		if verified[version.Generation] == nil {
			receipt, err := VerifyPackageInstallation(ctx, store, version.Generation, catalogue.limits)
			if err != nil {
				return nil, err
			}
			verified[version.Generation] = make(map[string]bool)
			for _, digest := range receipt.Digests {
				verified[version.Generation][digest] = true
			}
		}
		if !verified[version.Generation][version.Digest] {
			return nil, errors.New("registry references missing content")
		}
		key := packagePathKey(version.Ref)
		if source, exists := catalogue.refs[key]; exists && source != version.SourceID {
			return nil, errors.New("registry ref collision")
		}
		if catalogue.versions[version.SourceID] == nil {
			catalogue.versions[version.SourceID] = make(map[string]SkillVersion)
		}
		if _, exists := catalogue.versions[version.SourceID][version.Digest]; exists {
			return nil, errors.New("duplicate registry version")
		}
		for _, previous := range catalogue.versions[version.SourceID] {
			if previous.Ref != version.Ref {
				return nil, errors.New("inconsistent registry alias")
			}
			if !sameSourceProvenance(previous.Provenance(), version.Provenance()) {
				return nil, errors.New("inconsistent source provenance")
			}
		}
		catalogue.versions[version.SourceID][version.Digest] = version
		catalogue.refs[key] = version.SourceID
	}
	return catalogue, nil
}
