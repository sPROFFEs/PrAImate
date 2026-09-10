# Skills P0/P1 historical baseline — 2026-09-06

Current next steps: skills-implementation-checkpoint.md.

## P0 baseline

Initial checkout: `main`, `d60480482c257ad0f79d561c5657d32784aec66e`.
No tracked modifications initially. User-owned untracked files preserved:
AGENTS.md, cfg_css.svelte, cfg_modal.svelte, inject_code_edit.py, move_css.py,
opencode.json, patch_agents_mcp.py, patch_chats_ui.py, patch_clis.py,
patch_features.py, patch_modal.py, patch_modal2.py, patch_npm.py,
patch_ollama.py, patch_tabs.py, patch_workspace_chats.py, report.md,
test2.js, test_agent_tree.js, test_tree.js.

Toolchain: Go 1.26.1 linux/amd64, Node 20.19.2, npm 9.2.0.
Root module `git.jtsec.local/lab/PrAImate`; separate GUI module in
`cmd/praimate-gui` with local replace to root. Frontend: Svelte 4 / Vite 5.
The old Clade architecture/release knowledge is historical; actual checkout
has a GUI and managed runtime. No release actions authorized for this task.

## Verified entry points

| Surface | Actual route | Legacy skill behavior |
|---|---|---|
| Chats | GUI SendChat / SendChatStream / SendChatWithAttachments → ContinueChat(Stream) | GUI resolves flat IDs with ResolveSkillsPrefix; unknown IDs skipped |
| Document Studio | OpenEditorWindow → detachedCoordinator.openWithFolder; Editor → broker chat.send → main SendChatStream | Same chat prefix path, separate from editor file RPC |
| Agent Studio | StartAgentHelperChat → ephemeral agent-helper chat → SendChatStream | No agent-level skill binding; helper config is a chat |
| Code/Terminal | StartTerminal → ResolveExecutionConfig / PrepareExecution → exportAgentContext → termManager.start | Resolves prefix from argument list; RecordCodeSession does not persist that list |
| Legacy launcher | Plan / Decorate → skills.FetchAll → appendOnlineSkillsDirective; RestoreNativeSession | Separate URL bundles under native skills directory |
| Workflows | workflow_runner.go → SingleShot(Stream) / Resume(Stream); managed variant agentic_workflow.go | Agent instructions; no common skill plan |
| Headless | cmd/praimate/agents_cli.go + internal/core/external_agent_runs.go | Existing execution config / managed run routes; no shared skills resolver |
| Managed chat | continueManagedChat → RunManagedAgent per turn | New run and context per message; cannot retain a global loaded flag |

`internal/core/skills_user.go` imports ZIP Markdown by concatenation.
`internal/skills/skills.go` independently fetches bundles. These two legacy
paths must eventually converge on the package service, with explicit migration.
`ChatSettings.Skills` is a flat list; `ResumeOpts` has no system prompt.

## Evidence and boundaries

`internal/core/skills_legacy_baseline_test.go` preserves import bytes/order,
unknown-ID omission, first-turn adapter system prompt and resume message.
The fake adapter proves delivery to the adapter boundary, not native private
context or obedience. Existing TestStartAndContinueChat_ResumesSession also
checks native-session selection. No model calls are needed for these fixtures.

Executed: `go test ./internal/core -run TestSkillsLegacy -count=1` passed.
First sandbox attempt passed assertions but failed Go cache cleanup (exit 1);
rerun with cache-write approval exited 0. No production data used by new tests:
PRAIMATE_HOME and DB/workspace are temporary.

Available offline checks: root `go test ./internal/skills/...`,
`go test ./internal/core/... ./internal/agentic/...`,
`go test ./internal/launcher/...`; GUI separately `go test ./...` from its
module. Some tests use local HTTP servers, fake executables and local Git;
they need sandbox permissions but no paid provider. Frontend package.json has
test:endpoint-security, test:mcp-form, test:markdown, test:local-routing,
test:settings-ui, test:terminal, test:studio, test:surfaces (Node tests).
Production frontend build: `npm run build` (not_run this increment).
Baseline root `go build ./...`, `go vet ./...`, `go test ./...`: passed.
GUI `go test ./...`: passed. All eight frontend Node test files: passed.
Live GUI, real CLI, Windows and provider evaluations: **not_run**.

## P1 partial implementation

New files in internal/skills: package_digest.go/test, package_manifest.go/test,
package_inspect.go/test. No legacy callers changed or data migrated.
PackageDigest matches all three supplied sha256-tree-v1 vectors; references
and executable intent affect the digest. ParsePackageManifest uses yaml.v3,
preserves exact body bytes, accepts standard metadata and rejects aliases,
duplicate keys and excess size. InspectPackageZIP returns separate candidates,
excludes nested skills from parent inventories, and lists shared files for
explicit association. It performs no writes or execution.

`go test ./internal/skills -count=1`: passed after latest additions.
Digest contract tests were first run red (missing implementation), then green.
IMP-01: passed at unit inspection boundary. IMP-03: traversal rejection tested
at inspection boundary only. Other IMP acceptance cases remain not_run as
complete contracts, even where component tests cover part of the behavior.
P1 is incomplete; P2-P8 have not started. Do not expose this as installed/loaded.

Continuation: shared inventory validation now includes explicit and implicit
directories. Six regression cases first failed on the previous code, then
passed: case-mismatched parents, file/directory collisions in both orders,
duplicate directories, and Unicode simple-fold aliases (sigma). Digest uses
the same validator. Positive tests preserve explicit parents in either order.
ZIP tests also reject symlinks, FIFO/device entries, oversized SKILL.md and
corrupt CRC. No filesystem writes or activation added.
Verification after this increment: root build, vet, full tests and
`go test -race ./internal/skills` passed. GUI/Windows not rerun.
IMP-04/05/06 have additional component coverage, not complete acceptance:
local hardlinks, installation and platform-native validation remain pending.

Latest continuation: added host-owned PackageLimits and
InspectPackageZIPWithLimits; the original API retains defaults. Negative and
overflowing file limits fail closed. Reads use the smaller remaining expanded
budget before allocation, N+1 rejection, and context checks during decompression.
Tests cover exact limits and N+1 compressed/expanded/file/entry limits, invalid
configuration, and cancellation during a read. Skills race suite passed.
An arbitrary blocking ReaderAt still needs owner-provided I/O deadlines;
zip.NewReader allocates the central directory before entry-count validation.
Those limitations are not claimed solved. SKILL.md retains the parser's hard
256 KiB ceiling even if the host permits larger resource files.

Next: add bounded local acquisition and
transactional install/rollback, then secure pinned network source adapter.
Current path profile rejects combining marks conservatively; full Unicode
normalization handling and Windows native tests remain pending. Central-directory
allocation bounds and blocking source deadlines remain pending. No trust or
activation introduced. Read docs/02, docs/03, contracts/DIGEST.md and IMP cases;
FORGE/prototype bodies remain unread. No commit/push/release.
