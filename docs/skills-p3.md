# P3 — bindings and preview (complete)

## Update 2026-09-08

The increment-1 notes below are historical. Agent v2 YAML/DB persistence,
workflow selections, chat snapshots/defaults, pack bundles and pre-model gates
now exist. See `agent_skills_v2_test.go` for round-trip, trust exclusion,
fake-CLI blocking and saved-session defaults tests.

GUI: `cmd/praimate-gui/skills_v2_bindings.go` and tests expose strict agent/chat
preview and chat editing. `SkillBindingsEditor.svelte`, `api.js`, `Chats.svelte`
and `AgentStudio.svelte` connect them. Preview is resolution, not loading;
non-off v2 bindings block until runtime transport exists. Native contexts require
a fresh session for binding changes. New methods are not yet detached proxies.

Portable adaptation: YAML embeds skills and skills_lock; packs also carry
canonical JSON locks at skills.lock.json or skill-locks/*.json, provenance in
skill-bundles.json and objects at skills/<digesthex>.zip. Scope lock paths must
differ when their contents differ. No imported approvals. Retention is committed
before DB writes; failed imports may leave conservative unapproved objects/holds.
There is no single transaction across the registry, agent folders and DB.

Latest checks passed: root build/vet/full tests, GUI full tests, nine frontend
Node test files. Vite build passed with existing warnings before the final
one-line save-error handling change. Interactive GUI and native Windows not_run.
P3 acceptance is complete. Workflow lock round-trips, missing-bundle rollback,
strict v1/v2 rejection and atomic export validation are covered. P4 remains the
next phase: it must make delivery and context usage observable before any v2
binding can run. Native Windows execution and visual Wails QA are `not_run`, not
claims of cross-platform/runtime delivery.

## Historical increment 1

Status at that time: **P3 was in progress**. P2's backend is documented in
skills-p2.md. This increment implements the configuration/resolver contract and
a Core facade; it does not yet persist v2 bindings in agents/chats or wire GUI
actions. Existing agent/v1 and legacy chat execution remain unchanged.

## Files

| Files | Change |
| --- | --- |
| `internal/skills/config.go`, `config_test.go` | SkillConfig/Binding/Budget, strict JSON/YAML validation, independent preference snapshots. |
| `internal/skills/resolve.go`, `resolve_test.go` | Fixed scope precedence, host-policy intersection, installed-lock verification, structured diagnostics and atomic retention. |
| `internal/core/skills_resolution.go`, `skills_resolution_test.go` | Shared read-only Core preview and tests against the host resolver; legacy reader still rejects agent/v2. |
| `docs/skills-p3.md`, `skills-implementation-checkpoint.md` | Implemented scope, evidence and actual remaining work. |

## Semantics and safety boundaries

SkillScopes orders application → trusted project → agent → workflow → session.
The most specific configured scope replaces preferences, not host restrictions.
Nil or configured=false inherits. Configured=true with bindings=[] explicitly
selects none and does not restore defaults. Untrusted project preferences are
ignored with a diagnostic; accepting a different command does not trust them.

FreezeSkillPreferences returns copies of config/bindings/lock bytes. CreateChat
uses it to snapshot application/agent/session settings. A returned snapshot
cannot be changed by later mutation of caller buffers.

The portable SkillConfig shape matches praimate.skills-config/v1. JSON and YAML
validate required fields even when false/zero, enums, portable refs, duplicate
refs/keys, unknown fields and numeric limits. YAML rejects aliases, anchors,
custom tags and multiple documents. YAML follows P1's 4,096-node/16-depth profile;
JSON has a 20,000-node/16-depth profile. Both have a 4 MiB input ceiling. The host
must bound enclosing agent YAML before parsing it in the next increment.

Lockfile is a portable relative label, not permission to open a path. The host
passes already-read lock bytes. No config causes downloads, file writes outside
the host store, scripts or model calls. Untrusted data cannot supply host policy.

ResolveSkills returns eligible immutable versions, not activation receipts:

- off is not resolved or loaded, even if a version is absent;
- manual/auto/pinned must resolve the exact source, digest and provenance;
- current host review is required, including for own and built-in skills;
- host denials and verified transport capabilities apply after preferences;
- a required failure returns a SkillResolutionError with no usable partial set;
- optional failures omit that binding with an explicit diagnostic;
- results are deterministic by ref and do not alias catalogue state.

Diagnostic codes cover skill_not_found, integrity_mismatch, untrusted_source
and incompatible_transport. Infrastructure/parser failures can still be ordinary
Go errors; they also stop resolution. No raw package content is included in
diagnostic messages.

Host budget restrictions intersect component-wise; zero catalogue/resource caps
stay zero. RequireControlled cannot be weakened by a compatible preference.
**This computes limits, it does not enforce token usage.** Measuring payloads,
residency, max-active admission and load/read accounting are P4. No ceiling for
private native CLI context is claimed. Transport capability in tests is a host
fixture, not a compatibility certification for real adapters.

HostSkillStore.ResolveSkills is read-only and returns HostRevision. ResolveAndHold
revalidates and acquires retention in one revision-checked host transaction.
Failed required resolution leaves the host revision/state unchanged. A stale
preview cannot be committed. Core.PreviewSkillResolution uses this exact service;
GUI previews and headless execution gates use this service; runtime materialization
is explicitly deferred to P4.

## Tests and acceptance

| Criterion | Evidence now | Still pending |
| --- | --- | --- |
| BND-01 | TestFreezeSkillPreferencesPreservesExplicitEmptyAndIndependence; TestSkillResolutionScopePrecedenceAndHostIntersection | Persisting/consuming these values in actual sessions. |
| BND-02 | TestSkillResolutionUsesStableSnapshotsAndRetainsResolvedVersions; TestCoreSkillPreviewMatchesHostResolverWithoutMutatingState | Actual Chats, Studio, workflow and Terminal entry points. |
| BND-03 | TestSkillResolutionRequiredFailureIsAtomicAndOptionalDiagnosed | Actual pre-model runtime gates; these tests exercise the backend boundary only. |
| BND-04 | Same test plus TestSkillResolutionOffAndControlledPolicyCannotBeBypassed | Catalogue/UI delivery through actual adapters. |
| BND-05 | Not implemented in this increment | Agent-v2 pack import/export with locks, bundles and provenance. |
| BND-06 | TestSkillsAgentV2IsNotSilentlyAcceptedByLegacyReader | New version-aware reader/persistence and old-reader compatibility round-trip. |

Also passing: TestSkillConfigStrictWireContract and
TestSkillConfigYAMLUsesSameStrictContract. Full root build/vet/tests, skills race
tests and separate GUI module tests passed. Windows amd64 skills test binary
cross-compiled at /tmp/praimate-skills-p3-windows.test.exe; native execution is
not_run. No new UI controls, real model/CLI tests, installs or releases.

Commands:

```sh
go build ./...
go vet ./...
go test ./...
go test -race ./internal/skills -count=1
# cmd/praimate-gui:
go test ./...
# root, compile only:
GOOS=windows GOARCH=amd64 go test -c -o /tmp/praimate-skills-p3-windows.test.exe ./internal/skills
```

Next P3 increment: version-aware agent DTO/YAML/DB persistence, session/workflow
binding snapshots, self-contained pack validation/rollback and Agent Studio/chat
preview/settings integration. Do not add accepted fields that executors silently
ignore. Keep legacy/v2 routes distinct and fail closed until runtime delivery is
implemented. Full surface delivery remains P4–P6.
