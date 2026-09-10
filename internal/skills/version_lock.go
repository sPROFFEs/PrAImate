package skills

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
)

// SkillVersionLock is portable content identity, not configuration or authority.
// Its wire shape follows the kit's praimate.skills-lock/v1 contract. Agent pack
// assembly, scope inheritance and runtime loading remain separate operations.
type SkillVersionLock struct {
	Schema          string                  `json:"schema"`
	DigestAlgorithm string                  `json:"digest_algorithm"`
	Entries         []SkillVersionLockEntry `json:"entries"`
}
type SkillVersionLockEntry struct {
	Ref        string          `json:"ref"`
	SourceID   string          `json:"source_id"`
	Digest     string          `json:"digest"`
	Entrypoint string          `json:"entrypoint"`
	Source     SkillLockSource `json:"source"`
}
type SkillLockSource struct {
	Kind             string  `json:"kind"`
	Locator          string  `json:"locator"`
	ResolvedRevision *string `json:"resolved_revision"`
}

var portableSkillRef = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*$`)

func lockEntry(v SkillVersion) (SkillVersionLockEntry, error) {
	source := SkillLockSource{Kind: "local", Locator: v.SourceID}
	if v.SourceKind == "builtin" {
		source.Kind = "builtin"
	}
	if v.SourceKind == "external" && v.ResolvedRevision != "" {
		revision := v.ResolvedRevision
		source = SkillLockSource{Kind: "git", Locator: v.Origin, ResolvedRevision: &revision}
	}
	entry := SkillVersionLockEntry{Ref: v.Ref, SourceID: v.SourceID, Digest: v.Digest, Entrypoint: "SKILL.md", Source: source}
	return entry, validateLockEntry(entry)
}

func validateLockEntry(e SkillVersionLockEntry) error {
	if !portableSkillRef.MatchString(e.Ref) {
		return errors.New("lock ref must be a lowercase portable alias")
	}
	if err := validateRegistryIdentity(e.SourceID, e.Ref); err != nil {
		return err
	}
	if e.Entrypoint != "SKILL.md" || len(e.Digest) != 71 || !strings.HasPrefix(e.Digest, "sha256:") || strings.ToLower(e.Digest) != e.Digest {
		return errors.New("invalid lock content identity")
	}
	if _, err := hex.DecodeString(e.Digest[7:]); err != nil {
		return errors.New("invalid lock digest")
	}
	s := e.Source
	switch s.Kind {
	case "git":
		if s.ResolvedRevision == nil {
			return errors.New("git lock requires resolved commit")
		}
		if err := (SourceProvenance{Kind: "external", Origin: s.Locator, ResolvedRevision: *s.ResolvedRevision}).Validate(); err != nil {
			return err
		}
		if *s.ResolvedRevision == "" {
			return errors.New("git lock requires resolved commit")
		}
	case "local", "builtin":
		// Opaque identity, not a local filesystem path or a credential-bearing URL.
		if s.Locator != e.SourceID || s.ResolvedRevision != nil {
			return errors.New("local lock requires opaque source identity and null revision")
		}
	default:
		return errors.New("unsupported lock source")
	}
	return nil
}

// Reject duplicate keys before typed decoding. A small depth/node bound avoids
// ambiguous authority-shaped JSON and pathological imported metadata.
func validateLockJSON(body []byte) error {
	d := json.NewDecoder(bytes.NewReader(body))
	nodes := 0
	var value func(int) error
	value = func(depth int) error {
		nodes++
		if depth > 16 || nodes > 20000 {
			return errors.New("lock JSON complexity limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delim != '{' && delim != '[' {
			return errors.New("unexpected lock delimiter")
		}
		keys := make(map[string]bool)
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || keys[name] {
					return errors.New("duplicate lock field")
				}
				keys[name] = true
			}
			if err := value(depth + 1); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing lock data")
	}
	return nil
}

func DecodeSkillVersionLock(body []byte) (SkillVersionLock, error) {
	var lock SkillVersionLock
	if len(body) > maxRegistrySnapshot {
		return lock, errors.New("skill lock size limit exceeded")
	}
	if err := validateLockJSON(body); err != nil {
		return lock, err
	}
	type plain SkillVersionLock
	if err := decodePackageRecord(body, (*plain)(&lock)); err != nil {
		return SkillVersionLock{}, err
	}
	if lock.Schema != "praimate.skills-lock/v1" || lock.DigestAlgorithm != "sha256-tree-v1" || lock.Entries == nil || len(lock.Entries) > maxRegistryVersions {
		return SkillVersionLock{}, errors.New("invalid skill lock")
	}
	// resolved_revision is required even for local/null values.
	var presence struct {
		Entries []struct {
			Source map[string]json.RawMessage `json:"source"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(body, &presence); err != nil {
		return SkillVersionLock{}, err
	}
	seen := make(map[string]bool)
	for i, e := range lock.Entries {
		if presence.Entries[i].Source["resolved_revision"] == nil {
			return SkillVersionLock{}, errors.New("missing lock source revision field")
		}
		if err := validateLockEntry(e); err != nil {
			return SkillVersionLock{}, err
		}
		if seen[e.Ref] {
			return SkillVersionLock{}, errors.New("duplicate lock ref")
		}
		seen[e.Ref] = true
	}
	return lock, nil
}

func (tx *SkillHostTransaction) ExportLock(ctx context.Context, versions []SkillVersion) ([]byte, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	if len(versions) > maxRegistryVersions {
		return nil, errors.New("lock entry limit exceeded")
	}
	lock := SkillVersionLock{Schema: "praimate.skills-lock/v1", DigestAlgorithm: "sha256-tree-v1", Entries: []SkillVersionLockEntry{}}
	for _, v := range versions {
		actual, _, err := tx.catalogue.versionFiles(ctx, v.Ref, v.Digest)
		if err != nil {
			return nil, err
		}
		if actual != v {
			return nil, errors.New("lock version mismatch")
		}
		e, err := lockEntry(v)
		if err != nil {
			return nil, err
		}
		lock.Entries = append(lock.Entries, e)
	}
	sort.Slice(lock.Entries, func(i, j int) bool { return lock.Entries[i].Ref < lock.Entries[j].Ref })
	body, err := json.Marshal(lock)
	if err != nil {
		return nil, err
	}
	if _, err := DecodeSkillVersionLock(body); err != nil {
		return nil, err
	}
	return body, nil
}

// ExportLock is read-only; exporting does not advance the host revision.
func (s *HostSkillStore) ExportLock(ctx context.Context, versions []SkillVersion) ([]byte, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return nil, err
	}
	defer func() { tx.active = false }()
	return tx.ExportLock(ctx, versions)
}

// HoldLock validates against installed host versions only. Locator is never
// fetched or opened. Approval is neither required to retain bytes nor granted.
func (tx *SkillHostTransaction) HoldLock(ctx context.Context, owner string, body []byte) error {
	if err := tx.check(); err != nil {
		return err
	}
	lock, err := DecodeSkillVersionLock(body)
	if err != nil {
		return err
	}
	versions := make([]SkillVersion, 0, len(lock.Entries))
	for _, e := range lock.Entries {
		v, err := tx.catalogue.Resolve(e.Ref, e.Digest)
		if err != nil {
			return err
		}
		expected, err := lockEntry(v)
		if err != nil {
			return err
		}
		a, _ := json.Marshal(e)
		b, _ := json.Marshal(expected)
		if !bytes.Equal(a, b) {
			return errors.New("lock provenance mismatch")
		}
		versions = append(versions, v)
	}
	return tx.Hold(ctx, owner, versions)
}
