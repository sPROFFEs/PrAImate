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

var ErrSkillHostBusy = errors.New("skill store is busy; retry the operation")

// HostSkillStore owns a dedicated private directory. Callers must use this
// service, not mutate its files with the lower-level P1 primitives. Every
// transaction, restore and collection coordinates on one OS-held lock.
type HostSkillStore struct {
	root   *os.Root
	limits PackageLimits
}

type hostHead struct {
	Schema   string   `json:"schema"`
	Revision uint64   `json:"revision"`
	Current  string   `json:"current"`
	Retained []string `json:"retained"`
}
type hostCheckpoint struct {
	Schema     string            `json:"schema"`
	Catalogue  string            `json:"catalogue"`
	LocalState string            `json:"local_state"`
	Drafts     map[string]string `json:"drafts,omitempty"`
	Legacy     bool              `json:"legacy"`
}

func OpenHostSkillStore(directory string, limits PackageLimits) (*HostSkillStore, error) {
	limits, err := limits.normalized()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("skill store must be a directory, not a link")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		root.Close()
		return nil, errors.New("skill store changed while opening")
	}
	return &HostSkillStore{root: root, limits: limits}, nil
}
func (s *HostSkillStore) Close() error { return s.root.Close() }

func (s *HostSkillStore) lock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := s.root.OpenFile("host.lock", os.O_CREATE|os.O_RDWR|packageReadFlags, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	named, err := s.root.Lstat("host.lock")
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, named) || named.Mode()&os.ModeSymlink != 0 {
		f.Close()
		return nil, errors.New("invalid skill store lock")
	}
	if err := validatePackageLinkCount(f, info); err != nil {
		f.Close()
		return nil, err
	}
	if err := lockSkillHost(f); err != nil {
		f.Close()
		return nil, ErrSkillHostBusy
	}
	return func() { unlockSkillHost(f); f.Close() }, nil
}

func (s *HostSkillStore) read(ctx context.Context, name string, limit int64) ([]byte, error) {
	if err := validatePackagePath(name); err != nil || strings.Contains(name, "/") {
		return nil, errors.New("invalid host record path")
	}
	info, err := s.root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("non-regular host record")
	}
	body, _, err := readLocalPackageFile(ctx, s.root, name, info, limit)
	return body, err
}

func (s *HostSkillStore) checkpoint(ctx context.Context, id string) (hostCheckpoint, error) {
	var checkpoint hostCheckpoint
	if !strings.HasPrefix(id, "host-") || len(id) != 74 {
		return checkpoint, errors.New("invalid host checkpoint ID")
	}
	body, err := s.read(ctx, id, maxRegistrySnapshot)
	if err != nil {
		return checkpoint, err
	}
	hash := sha256.Sum256(body)
	if id != "host-"+hex.EncodeToString(hash[:])+".json" {
		return checkpoint, errors.New("host checkpoint integrity mismatch")
	}
	if err := decodePackageRecord(body, &checkpoint); err != nil {
		return checkpoint, err
	}
	if checkpoint.Schema != "praimate.skill-host/v1" {
		return checkpoint, errors.New("unsupported skill host checkpoint")
	}
	return checkpoint, nil
}

func (s *HostSkillStore) head(ctx context.Context) (hostHead, error) {
	body, err := s.read(ctx, "host-head.json", maxRegistrySnapshot)
	if errors.Is(err, os.ErrNotExist) {
		return hostHead{Schema: "praimate.skill-host-head/v1"}, nil
	}
	if err != nil {
		return hostHead{}, err
	}
	var head hostHead
	if err := decodePackageRecord(body, &head); err != nil {
		return head, err
	}
	if head.Schema != "praimate.skill-host-head/v1" || head.Current == "" || head.Revision == 0 || len(head.Retained) > maxRegistryVersions {
		return head, errors.New("invalid skill host head")
	}
	found := false
	for _, id := range head.Retained {
		if id == head.Current {
			found = true
		}
	}
	if !found {
		return head, errors.New("current skill checkpoint is not retained")
	}
	return head, nil
}

type SkillHostView struct {
	Revision   uint64
	Checkpoint string
	Legacy     bool
	Versions   []SkillVersion
	Drafts     map[string]string
	Retained   []string
}

type SkillHostTransaction struct {
	store     *HostSkillStore
	catalogue *VersionCatalogue
	local     *LocalSkillState
	drafts    map[string]string
	legacy    bool
	active    bool
	retained  []string
}

func (s *HostSkillStore) load(ctx context.Context, head hostHead) (*SkillHostTransaction, error) {
	tx := &SkillHostTransaction{store: s, drafts: make(map[string]string), legacy: true, active: true, retained: append([]string(nil), head.Retained...)}
	if head.Current == "" {
		var err error
		tx.catalogue, err = NewVersionCatalogue(s.root, s.limits)
		tx.local = NewLocalSkillState()
		return tx, err
	}
	checkpoint, err := s.checkpoint(ctx, head.Current)
	if err != nil {
		return nil, err
	}
	tx.catalogue, err = LoadVersionCatalogue(ctx, s.root, checkpoint.Catalogue, s.limits)
	if err != nil {
		return nil, err
	}
	tx.local, err = LoadLocalSkillState(ctx, s.root, checkpoint.LocalState, tx.catalogue)
	if err != nil {
		return nil, err
	}
	for key, id := range checkpoint.Drafts {
		if _, err := LoadSkillDraft(ctx, s.root, id, s.limits); err != nil {
			return nil, err
		}
		tx.drafts[key] = id
	}
	tx.legacy = checkpoint.Legacy
	return tx, nil
}

func (s *HostSkillStore) View(ctx context.Context) (SkillHostView, error) {
	unlock, err := s.lock(ctx)
	if err != nil {
		return SkillHostView{}, err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return SkillHostView{}, err
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return SkillHostView{}, err
	}
	tx.active = false
	view := SkillHostView{Revision: head.Revision, Checkpoint: head.Current, Legacy: tx.legacy, Drafts: tx.drafts, Retained: append([]string(nil), head.Retained...)}
	for _, versions := range tx.catalogue.versions {
		for _, version := range versions {
			view.Versions = append(view.Versions, version)
		}
	}
	sort.Slice(view.Versions, func(i, j int) bool {
		a, b := view.Versions[i], view.Versions[j]
		if a.Ref != b.Ref {
			return a.Ref < b.Ref
		}
		return a.Digest < b.Digest
	})
	return view, nil
}

// Update is a synchronous host transaction. The callback must not retain/use
// the transaction later or spawn concurrent callbacks. On error the old head
// remains authoritative; content/checkpoints already written are GC candidates.
func (s *HostSkillStore) Update(ctx context.Context, expectedRevision uint64, fn func(*SkillHostTransaction) error) (string, error) {
	if fn == nil {
		return "", errors.New("host transaction callback required")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock()
	head, err := s.head(ctx)
	if err != nil {
		return "", err
	}
	if head.Revision != expectedRevision {
		return "", errors.New("skill host revision conflict")
	}
	if head.Revision == ^uint64(0) || len(head.Retained) >= maxRegistryVersions {
		return "", errors.New("skill checkpoint history limit reached")
	}
	tx, err := s.load(ctx, head)
	if err != nil {
		return "", err
	}
	defer func() { tx.active = false }()
	if err := fn(tx); err != nil {
		return "", err
	}
	catalogue, err := tx.catalogue.SaveSnapshot(ctx)
	if err != nil {
		return "", err
	}
	local, err := tx.local.SaveCheckpoint(ctx, s.root)
	if err != nil {
		return "", err
	}
	checkpoint := hostCheckpoint{Schema: "praimate.skill-host/v1", Catalogue: catalogue, LocalState: local, Drafts: tx.drafts, Legacy: tx.legacy}
	body, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	id, err := saveImmutableRecord(ctx, s.root, "host", body)
	if err != nil {
		return "", err
	}
	head.Revision++
	head.Current = id
	found := false
	for _, previous := range head.Retained {
		if previous == id {
			found = true
		}
	}
	if !found {
		head.Retained = append(head.Retained, id)
	}
	if err := s.writeHead(ctx, head); err != nil {
		return "", err
	}
	return id, nil
}

func (s *HostSkillStore) writeHead(ctx context.Context, head hostHead) error {
	body, err := json.Marshal(head)
	if err != nil {
		return err
	}
	if len(body) > maxRegistrySnapshot {
		return errors.New("host head limit exceeded")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := ".head-stage-" + hex.EncodeToString(nonce[:])
	defer s.root.Remove(tmp)
	if err := writePackageObject(s.root, tmp, body); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.root.Rename(tmp, "host-head.json"); err != nil {
		return err
	}
	return syncPackageDirectory(s.root, ".")
}

func (tx *SkillHostTransaction) check() error {
	if !tx.active {
		return errors.New("skill transaction is closed")
	}
	return nil
}

// ResolveVersion returns verified identity and the CURRENT host approval under
// the same transaction lock used to acquire retention. It is not load evidence.
func (tx *SkillHostTransaction) ResolveVersion(ctx context.Context, ref, digest string) (SkillVersion, bool, error) {
	if err := tx.check(); err != nil {
		return SkillVersion{}, false, err
	}
	v, _, err := tx.catalogue.versionFiles(ctx, ref, digest)
	if err != nil {
		return SkillVersion{}, false, err
	}
	return v, tx.local.Approved(v), nil
}
func (tx *SkillHostTransaction) Publish(ctx context.Context, draft *SkillDraft, revision uint64) (SkillVersion, error) {
	if err := tx.check(); err != nil {
		return SkillVersion{}, err
	}
	if draft == nil {
		return SkillVersion{}, errors.New("draft required")
	}
	return draft.Publish(ctx, tx.catalogue, revision)
}
func (tx *SkillHostTransaction) Register(ctx context.Context, source, ref string, selected SelectedPackage) (SkillVersion, error) {
	return tx.RegisterSource(ctx, source, ref, selected, SourceProvenance{})
}

func (tx *SkillHostTransaction) RegisterSource(ctx context.Context, source, ref string, selected SelectedPackage, provenance SourceProvenance) (SkillVersion, error) {
	if err := tx.check(); err != nil {
		return SkillVersion{}, err
	}
	generation, err := InstallPackagesWithLimits(ctx, tx.store.root, []SelectedPackage{selected}, tx.store.limits)
	if err != nil {
		return SkillVersion{}, err
	}
	return tx.catalogue.RegisterWithProvenance(ctx, source, ref, generation, selected.Digest(), provenance)
}
func (tx *SkillHostTransaction) Approve(ctx context.Context, ref, digest string, approved bool) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.local.SetApproval(ctx, tx.catalogue, ref, digest, approved)
}
func (tx *SkillHostTransaction) Hold(ctx context.Context, owner string, versions []SkillVersion) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.local.Hold(ctx, tx.catalogue, owner, versions)
}

func (tx *SkillHostTransaction) Retain(ctx context.Context, owner string, versions []SkillVersion) error {
	if err := tx.check(); err != nil {
		return err
	}
	if previous, ok := tx.local.holds[owner]; ok {
		if len(previous) != len(versions) {
			return errors.New("retention identity conflict")
		}
		for i := range previous {
			if previous[i] != versions[i] {
				return errors.New("retention identity conflict")
			}
		}
		return nil
	}
	return tx.Hold(ctx, owner, versions)
}
func (tx *SkillHostTransaction) Release(owner string) error {
	if err := tx.check(); err != nil {
		return err
	}
	tx.local.Release(owner)
	return nil
}
func (tx *SkillHostTransaction) Forget(ref, digest string) error {
	if err := tx.check(); err != nil {
		return err
	}
	return tx.local.ForgetVersion(tx.catalogue, ref, digest)
}
func (tx *SkillHostTransaction) SaveDraft(ctx context.Context, key string, draft *SkillDraft) error {
	if err := tx.check(); err != nil {
		return err
	}
	if key == "" || len(key) > 255 || draft == nil {
		return errors.New("invalid draft slot")
	}
	if len(tx.drafts) >= maxRegistryVersions && tx.drafts[key] == "" {
		return errors.New("draft slot limit exceeded")
	}
	id, err := draft.SaveCheckpoint(ctx, tx.store.root)
	if err != nil {
		return err
	}
	tx.drafts[key] = id
	return nil
}
func (tx *SkillHostTransaction) LoadDraft(ctx context.Context, key string) (*SkillDraft, error) {
	if err := tx.check(); err != nil {
		return nil, err
	}
	return LoadSkillDraft(ctx, tx.store.root, tx.drafts[key], tx.store.limits)
}

func (tx *SkillHostTransaction) DeleteDraft(key string) error {
	if err := tx.check(); err != nil {
		return err
	}
	delete(tx.drafts, key)
	return nil
}

// Legacy mode never rewrites existing chats or implicitly enables v2 bindings.
func (tx *SkillHostTransaction) SetLegacyMode(legacy bool) error {
	if err := tx.check(); err != nil {
		return err
	}
	tx.legacy = legacy
	return nil
}

// Restore restores catalogue/drafts/mode, never historic approvals. Current
// leases must still resolve; rollback cannot silently discard an active run.
func (tx *SkillHostTransaction) Restore(ctx context.Context, id string) error {
	if err := tx.check(); err != nil {
		return err
	}
	retained := false
	for _, previous := range tx.retained {
		if previous == id {
			retained = true
		}
	}
	if !retained {
		return errors.New("checkpoint is not retained")
	}
	checkpoint, err := tx.store.checkpoint(ctx, id)
	if err != nil {
		return err
	}
	catalogue, err := LoadVersionCatalogue(ctx, tx.store.root, checkpoint.Catalogue, tx.store.limits)
	if err != nil {
		return err
	}
	for _, versions := range tx.local.holds {
		for _, held := range versions {
			actual, err := catalogue.Resolve(held.Ref, held.Digest)
			if err != nil || actual != held {
				return errors.New("rollback would invalidate a retained version")
			}
		}
	}
	drafts := make(map[string]string)
	for key, id := range checkpoint.Drafts {
		if _, err := LoadSkillDraft(ctx, tx.store.root, id, tx.store.limits); err != nil {
			return err
		}
		drafts[key] = id
	}
	known := make(map[string]bool)
	for _, versions := range catalogue.versions {
		for _, version := range versions {
			known[versionAuthorityKey(version)] = true
		}
	}
	for key := range tx.local.approvals {
		if !known[key] {
			delete(tx.local.approvals, key)
		}
	}
	tx.catalogue = catalogue
	tx.drafts = drafts
	tx.legacy = checkpoint.Legacy
	return nil
}

func (tx *SkillHostTransaction) MigrateLegacy(ctx context.Context, plan *LegacyMigrationPlan, acceptDiagnostics bool) (LegacyMigrationResult, error) {
	if err := tx.check(); err != nil {
		return LegacyMigrationResult{}, err
	}
	before, err := tx.catalogue.SaveSnapshot(ctx)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	merged, err := LoadVersionCatalogue(ctx, tx.store.root, before, tx.store.limits)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	result, err := ApplyLegacyMigration(ctx, tx.store.root, plan, acceptDiagnostics, tx.store.limits)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	migrated, err := LoadVersionCatalogue(ctx, tx.store.root, result.CatalogueSnapshot, tx.store.limits)
	if err != nil {
		return LegacyMigrationResult{}, err
	}
	for _, versions := range migrated.versions {
		for _, version := range versions {
			if _, err := merged.RegisterWithProvenance(ctx, version.SourceID, version.Ref, version.Generation, version.Digest, version.Provenance()); err != nil {
				return LegacyMigrationResult{}, err
			}
		}
	}
	tx.catalogue = merged
	return result, nil
}
