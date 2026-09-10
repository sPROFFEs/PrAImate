package skills

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"sync"
)

// LocalSkillState is host authority/retention, deliberately separate from
// packages, registry snapshots and portable exports. Never deserialize imported
// SKILL.md, agent YAML or a skill lock into this type.
type LocalSkillState struct {
	mu        sync.Mutex
	approvals map[string]bool
	holds     map[string][]SkillVersion
}

func NewLocalSkillState() *LocalSkillState {
	return &LocalSkillState{approvals: make(map[string]bool), holds: make(map[string][]SkillVersion)}
}
func versionAuthorityKey(v SkillVersion) string { return v.SourceID + "\x00" + v.Digest }

// SetApproval is a host-only action. Approval is for this source and digest,
// not executable permission and not automatic approval of subsequent versions.
func (s *LocalSkillState) SetApproval(ctx context.Context, c *VersionCatalogue, ref, digest string, approved bool) error {
	v, err := c.Resolve(ref, digest)
	if err != nil {
		return err
	}
	if approved {
		if _, err := VerifyPackageInstallation(ctx, c.store, v.Generation, c.limits); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if approved {
		s.approvals[versionAuthorityKey(v)] = true
	} else {
		delete(s.approvals, versionAuthorityKey(v))
	}
	return nil
}

func (s *LocalSkillState) Approved(v SkillVersion) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.approvals[versionAuthorityKey(v)]
}

// Hold retains exact versions for a host-owned lock, checkpoint, run or native
// session. There is no automatic TTL: a crashed session must not lose its data.
func (s *LocalSkillState) Hold(ctx context.Context, c *VersionCatalogue, owner string, versions []SkillVersion) error {
	if len(owner) == 0 || len(owner) > 255 || len(versions) == 0 || len(versions) > maxRegistryVersions {
		return errors.New("invalid retention hold")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.holds[owner]; exists {
		return errors.New("retention owner already exists")
	}
	if len(s.holds) >= maxRegistryVersions {
		return errors.New("retention owner limit exceeded")
	}
	for _, requested := range versions {
		if err := ctx.Err(); err != nil {
			return err
		}
		actual, err := c.Resolve(requested.Ref, requested.Digest)
		if err != nil {
			return err
		}
		if actual != requested {
			return errors.New("retention version mismatch")
		}
		if _, err := VerifyPackageInstallation(ctx, c.store, actual.Generation, c.limits); err != nil {
			return err
		}
	}
	s.holds[owner] = append([]SkillVersion(nil), versions...)
	return nil
}

func (s *LocalSkillState) Release(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.holds, owner)
}

// ForgetVersion removes a catalogue reference, not filesystem content. Actual
// object GC must additionally retain versions in every saved checkpoint and
// coordinate across processes; this method does not pretend to provide that.
func (s *LocalSkillState) ForgetVersion(c *VersionCatalogue, ref, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	source, exists := c.refs[packagePathKey(ref)]
	if !exists {
		return errors.New("skill ref not found")
	}
	v, exists := c.versions[source][digest]
	if !exists {
		return errors.New("skill version not found")
	}
	for _, held := range s.holds {
		for _, pin := range held {
			if versionAuthorityKey(pin) == versionAuthorityKey(v) {
				return errors.New("skill version is retained by a lock or lease")
			}
		}
	}
	delete(c.versions[source], digest)
	if len(c.versions[source]) == 0 {
		delete(c.versions, source)
		delete(c.refs, packagePathKey(ref))
	}
	delete(s.approvals, versionAuthorityKey(v))
	return nil
}

type localSkillRecord struct {
	Schema   string                    `json:"schema"`
	Approved []string                  `json:"approved,omitempty"`
	Holds    map[string][]SkillVersion `json:"holds,omitempty"`
}

func (s *LocalSkillState) SaveCheckpoint(ctx context.Context, privateStore *os.Root) (string, error) {
	if privateStore == nil {
		return "", errors.New("private host state store required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := localSkillRecord{Schema: "praimate.local-skill-state/v1", Holds: s.holds}
	for key := range s.approvals {
		record.Approved = append(record.Approved, key)
	}
	sort.Strings(record.Approved)
	body, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if len(body) > maxRegistrySnapshot {
		return "", errors.New("local state limit exceeded")
	}
	return saveImmutableRecord(ctx, privateStore, "local-state", body)
}

// LoadLocalSkillState is only for a host-selected private checkpoint, never
// for an imported package/lock. Held content is verified again before return.
func LoadLocalSkillState(ctx context.Context, privateStore *os.Root, name string, c *VersionCatalogue) (*LocalSkillState, error) {
	if privateStore == nil || c == nil {
		return nil, errors.New("host state store and catalogue required")
	}
	if len(name) != 81 || !strings.HasPrefix(name, "local-state-") || !strings.HasSuffix(name, ".json") {
		return nil, errors.New("invalid local state checkpoint")
	}
	info, err := privateStore.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("non-regular local state")
	}
	body, _, err := readLocalPackageFile(ctx, privateStore, name, info, maxRegistrySnapshot)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	if name != "local-state-"+hex.EncodeToString(hash[:])+".json" {
		return nil, errors.New("local state integrity mismatch")
	}
	var record localSkillRecord
	if err := decodePackageRecord(body, &record); err != nil {
		return nil, err
	}
	if record.Schema != "praimate.local-skill-state/v1" || len(record.Holds) > maxRegistryVersions || len(record.Approved) > maxRegistryVersions {
		return nil, errors.New("invalid local state")
	}
	s := NewLocalSkillState()
	for owner, versions := range record.Holds {
		if err := s.Hold(ctx, c, owner, versions); err != nil {
			return nil, err
		}
	}
	c.mu.RLock()
	known := make(map[string]bool)
	for _, versions := range c.versions {
		for _, version := range versions {
			known[versionAuthorityKey(version)] = true
		}
	}
	c.mu.RUnlock()
	for _, key := range record.Approved {
		if !known[key] {
			return nil, errors.New("approval references an unknown version")
		}
		s.approvals[key] = true
	}
	return s, nil
}
