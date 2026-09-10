# P2 — registry, authoring and immutable versions

Implementation/evidence: 2026-09-07, Linux amd64, Go 1.26.1. This increment
provides the common **backend service**. It does not activate v2 skills in legacy
chats or claim that a model/CLI loaded instructions. Bindings and actual surface
delivery follow in P3–P6, as required by the implementation plan.

## Changed files

| Files | Responsibility |
| --- | --- |
| `internal/skills/registry.go`, `registry_snapshot.go`, corresponding tests | Immutable source/ref/digest identities, collisions and verified durable catalogue snapshots. |
| `internal/skills/draft.go`, `draft_snapshot.go`, corresponding tests | Editable drafts, optimistic revisions, exact-byte checkpoints and validated publication. |
| `internal/skills/provenance.go`, `version_content.go`, `version_content_test.go` | Origin metadata, forks, exact per-file update diff, verified reads and package ZIP export. |
| `internal/skills/local_state.go`, `local_state_test.go` | Host-only approvals and persistent retention owners, separate from execution permissions. |
| `internal/skills/host_store.go`, `host_lock_linux.go`, `host_lock_windows.go`, `host_lock_other.go`, `host_store_test.go` | Cross-process transactions, active checkpoint, draft slots, rollback and crash recovery. |
| `internal/skills/host_gc.go` | Dry-run/explicit GC coordinated with publication and retained checkpoints. |
| `internal/skills/version_lock.go`, `version_lock_test.go` | Strict portable locks, installed-only validation and retention; no imported authority. |
| `internal/skills/legacy_migration.go`, `legacy_migration_test.go` | Literal backup, deterministic conversion, override mapping, diagnostics and idempotency. |
| `internal/core/skills_migration.go`, `skills_store.go`, corresponding tests | Actual built-in/skills.json adapter and canonical application-directory service. |
| `docs/skills-p2.md`, `skills-implementation-checkpoint.md` | API, evidence, guarantees and next phase. |

Most files are new/untracked: `git diff` alone is not the full change. Existing
runtime callers/settings and the user's unrelated files are unchanged. The
existing P1 go.mod dependency change is not a new dependency of this increment.

## Host API and transaction rules

`core.OpenSkillStore(limits)` opens `<appdata.Root()>/skills-v2`: PRAIMATE_HOME,
Linux's XDG config location or Windows's APPDATA location. It is inside owned
application data, not a project/CLI directory. No logging subsystem or model
call is introduced.

Use `HostSkillStore` for all live operations. Never call standalone P1 installers,
catalogues or local-state methods directly against its private folder; their
process-local locks do not coordinate with host GC.

- `View`: verified catalogue, revision, mode, drafts and retained checkpoints.
- `Update(ctx, expectedRevision, callback)`: synchronous transaction; stale
  revisions and OS lock contention fail explicitly. Refresh/retry. Do not retain
  the transaction or use it from concurrent goroutines.
- Transaction methods: Publish, RegisterSource, SaveDraft, LoadDraft, Fork,
  Approve, ResolveVersion, Hold/HoldLock, Release, Forget, MigrateLegacy,
  Restore and SetLegacyMode.
- `ReadVersion`: verified copies, never mutable store paths.
- `PreviewUpdate`: pinned installed digest versus a reviewed P1 selection;
  exact bytes and executable intent for instructions, scripts and resources.
  FileChange.Before/After are side-by-side values; nil means absent. No LLM
  rewrite or hidden publication.
- `ExportPackageZIP` and `ExportLock`: package/identity only, no revision change.
- `Collect(ctx, revision, dryRun)` and `ReleaseCheckpoint`: explicit GC/history
  pruning, never automatic loss of rollback content.

An operation takes flock on Linux or LockFileEx on Windows, reloads the current
state, installs immutable content, writes immutable catalogue/authority/draft
records, and replaces host-head.json last. Failed callbacks leave the old head
authoritative. Process exit releases the OS lock; orphaned complete generations
are later GC candidates. A stage directory existing is not a committed install.

All selected checkpoints are retained by default. GC marks generations from
every retained catalogue and validates every authority checkpoint, including
ones sharing a catalogue. Holds retain exact versions for a lock, interrupted
run or native session. No TTL silently releases a crashed session. Forget fails
while any owner holds the version. After releasing owners and forgetting it,
historical checkpoints still protect content until explicitly pruned. GC never
blanket-deletes stages, draft records, migration backups or other host files.
Automatic cleanup of inert stage directories is deferred.

Bounds: P1 package limits; 1,000 catalogue versions, retention owners, draft slots
and retained host checkpoints; 4 MiB registry/authority/lock records; 16 MiB draft
records with an 8 MiB content cap; 100,000 GC inventory entries. Excesses fail,
not truncate. Verification has I/O cost; no background polling has been added.

## Identity, provenance and authority

Display name is not identity. SourceID, namespaced Ref and full tree Digest are
separate. NewOwnSkillSourceID generates a random identity; ExternalSkillSourceID
hashes HTTPS origin plus subpath independently of commit. Use the canonical
repository returned by P1's Git adapter. The generic origin helper folds host
case, not guessed forge-specific URL equivalences.

RegisterSource receives host-reviewed provenance, not deserialized permission
objects. Kind/origin/subpath/fork identity cannot be rebound. Reimporting identical
bytes is idempotent even if a later Git commit contains them; the original
immutable receipt remains unchanged. A new digest never inherits approval.

Fork copies verified bytes/licenses/resources, creates an own identity and
records DerivedSource/DerivedDigest (the backend's derived_from representation).
Draft checkpoints preserve that provenance; publishing never edits its parent.
Editable draft revisions do not change any published object.

Approvals are private local-state records, not manifests, registry snapshots or
locks. Review approval is not script execution permission. Restore preserves
current authority, never resurrects revoked approvals and refuses to invalidate
current retention owners. Later phases must not expose these host actions as
model-editable tools.

## Portable export/lock profile

ZIP export serializes only verified PackageFile bytes and executable intent;
round-trip tests reproduce the digest/license. Host secrets, sessions, history,
leases and approvals are not enumerated. **Secrets authored inside a package
remain package content**: review ReadVersion before sharing. No automatic secret
scanner or silent redaction is claimed.

The lock wire shape implements the proposed praimate.skills-lock/v1 and
sha256-tree-v1. Unknown fields at every level, duplicate JSON keys/refs, missing
required fields, bad hashes and credential-bearing Git URLs fail. HoldLock
validates against installed content and provenance only: locator never triggers
a download or filesystem access. Imported trust/permission flags are rejected.

Explicit profile decisions, stricter than the proposed schema:

- Portable aliases are lowercase ASCII namespace/name; older internal aliases
  remain readable but fail portable export if outside that profile.
- Local/built-in locator is opaque source identity, not an absolute path;
  resolved_revision is null. Locally acquired non-Git archives use this profile.
- Git requires credential-free HTTPS and a full lowercase 40-hex commit, matching
  P1's GitHub adapter. The schema's Git SHA-256 option is not yet supported.
- Additional fork/subpath provenance remains in the host version: the proposed
  lock has no such fields. SourceID still distinguishes forks/subpaths. P3 pack
  assembly must preserve full portable provenance separately, never export the
  private host checkpoint as a pack.

Agent-pack assembly and session/scope bindings are P3, not implicitly implemented
by this portable content-lock primitive.

## Migration/rollback

Create/retain a host checkpoint before migration. core.PreviewStoredLegacySkills
takes a bounded, confined literal snapshot of actual skills.json plus built-ins.
Review Entries(), then apply the **same plan** with tx.MigrateLegacy. Invalid
entries retain diagnostics; partial migration requires explicit acknowledgement.
Separate identities preserve user overrides. The transaction adds to existing
own/external entries and saves the literal original plus mapping/diagnostics.

Reapplying reuses identical content and checkpoint IDs; concurrency revision
advances for an accepted operation. Restore(previousID) restores catalogue,
drafts and mode. The original file was never altered and needs no copying back.
Legacy defaults true. Setting that preference does not enable an unimplemented
v2 runtime or rewrite chat settings. No old checkbox becomes a pinned binding.
No real user data was migrated during development.

## Acceptance evidence

| Criterion | Passing P2 backend evidence | Boundary |
| --- | --- | --- |
| REG-01 | TestVersionCatalogueSeparatesSourcesAndPinsVersions | Same-name bundles keep separate identities and collision-safe aliases. |
| REG-02 | TestDraftPublishDoesNotMutatePinnedVersion; TestHostUpdateForkExportAndRevocation | Pin A and its exact bytes survive publishing B. Real Chat v2 binding/delivery belongs to P3/P5. |
| REG-03 | TestPortableLockCannotImportAuthorityAndRetainsInterruptedRuns; TestRegistrySnapshotRejectsImportedTrust | Lock/registry authority poisoning rejected. Full agent-pack import is P3. |
| REG-04 | TestHostStoreAtomicRestartRetentionAndGC; TestPortableLockCannotImportAuthorityAndRetainsInterruptedRuns | Persisted run/native-owner fixtures and historical checkpoints protect content. Real native adapter acquisition is P6. |
| REG-05 | TestStoredSkillMigrationIsAdditiveIdempotentAndReversible | Actual built-ins, override, invalid entry, pre-existing own skill; apply twice, revert and verify original bytes. |

Additional tests cover cross-process contention, abrupt process termination after
installation, every retained authority checkpoint, tampered resource export,
rollback after approval revocation, retention bounds and nonregular legacy input.

Executed/passed:

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./internal/skills -count=1
# cmd/praimate-gui:
go test ./...
# cmd/praimate-gui/frontend:
node --test src/lib/endpointSecurity.test.js src/lib/mcpForm.test.js src/lib/markdown.test.js src/lib/localRouting.test.js src/pages/settingsUi.test.js src/lib/terminal.test.js src/pages/AgentStudio.test.js src/pages/surfaceNavigation.test.js
# repository root, compile only:
GOOS=windows GOARCH=amd64 go test -c -o /tmp/praimate-skills-p2-windows.test.exe ./internal/skills
```

Frontend: 8/8 files passed. GUI regression tests are not a browser smoke test of
new authoring controls (none added in P2). Windows native execution/crash tests
and real CLI/model skill delivery are not_run. Linux process-crash recovery is
tested; Windows directory fsync is unavailable in P1, so Windows power-loss
durability is not promised. No installs, paid calls, commits, pushes or releases.

P2 backend implementation is complete within these phase boundaries. Next is
P3 bindings/agent-pack integration, using this service and retaining legacy mode.
