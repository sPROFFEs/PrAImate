# P1: package backend and acceptance evidence

This is the new backend foundation, not a change to the existing Skills GUI or
runtime. Legacy callers and user data remain unchanged. Installing content does
not enable it, grant trust, load instructions or execute scripts. P2–P6 provide
those separate registry/binding/runtime integrations.

## API and supported sources

All APIs live in `internal/skills`:

1. `InspectPackageDirectory(ctx, directory, limits)` or
   `InspectPackageZIPWithLimits(ctx, readerAt, size, limits)` returns separate
   candidates and shared repository paths. Snapshot bytes stay private.
2. `FetchPackageZIP` provides bounded HTTPS acquisition. `FetchGitHubPackages`
   resolves a repository root, complete ref and subpath independently, looks up
   the default branch if omitted, resolves a full commit and downloads that
   commit's archive. GitHub is the implemented forge adapter; arbitrary Git,
   GitLab/Gitea APIs, SSH and authentication are not implicitly supported.
3. `SelectPackages` requires preview digests and explicit shared associations.
   Shared licenses/resources change the final digest. It never fetches again.
4. `InstallPackagesWithLimits` writes a new content generation beneath a private
   host-owned `os.Root`. `VerifyPackageInstallation` is mandatory before using
   a reopened generation. Match the caller's expected generation/digests too.

The GitHub adapter follows the documented [commit endpoint](https://docs.github.com/en/rest/commits/commits#get-a-commit)
and [archive endpoint](https://docs.github.com/en/rest/repos/contents#download-a-repository-archive-zip).
TLS and the host's network policy authenticate transport; a tree digest is not
an author signature or permission to run a skill.

## Transaction decision

P1 uses co-located objects, executable-intent inventories and a content receipt
inside one generation. This avoids a split object/receipt transaction: one
same-filesystem directory rename publishes them together. Generation identity
is the first 128 bits of SHA-256 over the canonical, sorted content receipt.
Full SHA-256 tree digests remain in the receipt and are always verified.

Reinstalling an identical set is idempotent, including concurrent callers.
Corrupt existing generations fail closed rather than being repaired silently.
Rollback at this layer means selecting/reinstalling the previous content set;
it returns the original generation and leaves both versions intact. P2 must
transactionally update its own references only after installation succeeds.
There is deliberately no global active-generation pointer or implicit enable.

An ordinary failure removes only that call's private staging directory. An
abrupt process exit can leave an inert `.stage-*` orphan. It is never accepted
as installed; retry can publish the complete generation. Do not automatically
delete every stage: another process may still own one. Orphan reclamation needs
the later lease/GC lifecycle. Linux flushes files, child directories and the
parent around publication. Windows files are flushed, but power-loss durability
of directory publication is **not claimed**; native Windows tests remain required.

## Security and compatibility profile

- Default bounds: 25 MiB download, 100 MiB expanded/selected, 5000 entries,
  5 MiB resource, 256 KiB SKILL.md. Host limits also apply at install/reopen.
- ZIP directory framing is checked before `archive/zip` builds its entry list.
  Multi-disk, ZIP64, self-extracting and Unix-link-extra formats are rejected.
  CRC/decompression remain the standard ZIP reader's responsibility.
- NFC paths are preserved, not rewritten; compatibility normalization/full case
  folding detect collisions. Reserved Windows names, traversal, invalid UTF-8,
  links, unexpected installed resources and excessive depth/length are rejected.
- Directory acquisition checks opened identities and link counts. It is not an
  atomic filesystem snapshot or a defense against a malicious same-user process
  controlling the host store. `os.Root` does not exclude bind mounts.
- Network policy disables environment proxies, checks all DNS answers and dials
  a checked IP. Redirects get the same policy. Private origin approvals and CA
  roots are host-owned; imported content cannot extend them. No TLS bypass.
- Scripts are stored without execute bits; executable intent is hashed and
  separately preserved so Windows does not infer it from missing POSIX bits.
- No git executable, hooks, submodules, filters, LFS or build commands are used.

## Acceptance matrix (Linux execution)

| Case | Evidence | Status/boundary |
|---|---|---|
| IMP-01 | `TestInspectPackageZIPSeparateCandidates` | passed |
| IMP-02 | `TestInstallPackagesPreservesResourcesAndRollback`, script/assets fixture | passed |
| IMP-03 | escape and tampered-plan tests | passed; reject before writing |
| IMP-04 | local links, ZIP special entries/Unix metadata and path tests | Linux passed; Windows execution not_run |
| IMP-05 | ambiguous inventory and Unicode collision tests | passed |
| IMP-06 | host exact/N+1 limits, parser bounds, ZIP preflight | passed for tested bounded profile |
| IMP-07 | `TestPackageImportInstallNeverExecutesScript` | import/install passed; enabling belongs to later bindings and is not tested here |
| IMP-08 | `TestGitSourcePinsDefaultAndSlashRefsThroughInstallation` | passed with HTTPS fixture, GitHub adapter |
| IMP-09 | private snapshot and pinned-install fixtures | passed; install makes no fetch |
| IMP-10 | `TestPackageRedirectDoesNotReadUnapprovedDestination` | passed; loopback/link-local destination not read |
| IMP-11 | `TestSelectPackagesPreservesSharedBytesAndDigest` | passed |
| IMP-12 | crash subprocess, retry, prior-version and corruption tests | process-crash recovery passed; not a power-cut test |

Run `go test -race ./internal/skills -count=1`, root build/vet/tests and the
separate GUI module tests. Run `go test ./internal/skills -count=1` on Windows;
cross-compiling that suite is not evidence that it ran there. Fixture servers
and child processes are local; these tests do not use a paid model.

P1's backend is implemented for this explicit profile. Full cross-platform
acceptance is not declared until the Windows run and later enable-path check
are available. No runtime skill delivery, model obedience or FORGE result is
claimed by this phase.
