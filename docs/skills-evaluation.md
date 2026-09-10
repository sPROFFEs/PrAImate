# Skills v2 evaluation record — 2026-09-09

Repository: `/home/parrot/projects/praimate`, main at `d60480482c257ad0f79d561c5657d32784aec66e` plus the uncommitted skills implementation and test-readiness review.

**Verdict: controlled-path test candidate, not P0–P8 feature-complete or release-ready.**
This review supersedes the previous blanket “passed” and “complete” claims.
A library test does not prove a GUI flow, a real CLI, or model behavior.

Scope: offline deterministic tests and fake-adapter transport tests only. No
external skill was downloaded, no package script was run, no model credential
was used, and no deployment/commit/publish action was performed.

## Evidence matrix

| Kit cases | Evidence | Result / coverage |
|---|---|---|
| IMP-01…03, 05…12 | `package_*_test.go`: candidates, resources, escape/collision/limit rejection, no execution, pinned refs, shared associations, crash recovery | passed at library level, not an end-to-end GUI importer test |
| IMP-04 | local directory/link and ZIP special-file tests | Linux passed; Windows execution not_run |
| REG-01…05 | registry, draft, lock, migration, library command and GC tests | backend passed; library UI now wired for import/author/fork/publish/export and reviewed legacy copy; visual QA not_run |
| BND-01, 03…06 | strict config/lock, reviewed portable v2 pack and legacy-reader tests | deterministic backend cases passed; one content-bound import review installs, approves and binds exact bundled digests |
| BND-02 | shared resolver and workflow/managed payload tests | partial: common resolver verified; production native adapters not characterized |
| CTX-01…10 | runtime tests; managed load/read transport, request/attachment budget tests | controlled-payload cases passed; compaction is library-level, not live CLI history evidence |
| CTX-11 | durable encrypted task counters, atomic reservations, final prepared payload accounting, resume/retry scope | controlled input bytes implemented across chat/workflow/external attempts; not provider tokens, cost or private CLI usage |
| CTX-12…13 | tokenizer-unavailable rejection, new-payload budget and coverage tests | estimates/coverage passed; exact tokenizer and native full-request visibility not implemented |
| CTX-14 | workflow finish_evidence artifact contract and persisted resume gate | artifact presence/size/optional exact hash verified; no command/revision freshness verifier or general code-quality verdict |
| SEC-01…02 | approved exact locks, host broker, optional-revocation test, imported-authority rejection | deterministic checks passed; no privileged skill.run tool |
| SEC-03 | sanitized receipts and resource text excluded from managed tool transcript | partial: adversarial real-model behavior evaluation not_run |
| UI-01 | Core transport, GUI binding and frontend bridge/source tests | partial: actual Wails create/edit/reopen interaction not_run |
| UI-02 | one versioned library, installed-version picker, agent-pack review and advanced config+lock editor, including scoped Studio windows | legacy authoring/pickers removed from normal UX; visual end-to-end not_run |
| UI-03 | shared Core resolver, unchanged CLI result contract | partial: dedicated v2 headless-versus-GUI process comparison not_run |
| NAT-01…07 | fake materialization plus exclusive terminal context/cleanup tests | compatible fallback passes: exact pinned bodies + private resources are materialized for all supported terminal CLIs; native reads remain unknown, not acceptance-passed |
| FOR-01 | actual local kit imported, portable pack round-trip and review digest captured by fake adapter | transport passed (9 skills/7 workflows); plain FORGE launches bind three common skills and workflows bind their specialist; real review quality not_run |
| FOR-02…04 | fixture policy and isolated fake calls only | real behavior not_run |
| EXT-01…03 | synthetic original/fork/delegation test | partial: actual Ponytail/Matt revisions, licenses and adaptations not acquired/validated |
| REL-01 | `internal/core/skills_rollout_test.go` | passed: v2 off is explicit/no adapter call/no data mutation; legacy chat continues; re-enable restores exact pinned payload |
| REL-02 | no authorized model benchmark | not_run |

The kit's source `acceptance-cases.json` remains an acceptance plan rather than
being rewritten as if it were an executed result. The mapping above is the
PrAImate evidence record.

## Cost and quality comparison

| Variant | Model / adapter | Tasks accepted | Input/output/cached tokens | Cost | Result |
|---|---|---:|---|---|---|
| A baseline FORGE | not authorized | null | unknown | unknown | not_run |
| B selected v2 FORGE | fake controlled adapter | not applicable | unknown | unknown | transport only |
| C progressive v2 FORGE | fake controlled adapter | not applicable | bounded receipt estimates only | unknown | transport only |
| D all skills stress | not run | null | unknown | unknown | not_run |

No byte count is reported as provider tokens or monetary cost. A future paired
benchmark needs an authorized model, fixed project SHA/permissions/model,
alternated cold and warm repetitions, accepted-task criteria and all failed
runs included in cost per accepted task.

## Release gates and support boundary

Executed deterministic suites pass; that does **not** make every critical
acceptance case green. CTX-11/14 and the UI/native/external gaps above remain
release blockers for the complete kit scope. V2 stays local opt-in, default
off. No benchmark, release or external skill distribution was performed.

## Test-readiness fixes

- `runtime.go`: active count counts bodies, not estimated tokens; whole-plan
  failure and dependency/conflict closure roll back; resident loads do not
  redeliver; resource pages retain distinct reservations and cursors.
- `managed_skills.go` / `agentic_run.go`: managed model and skill broker share
  a runtime. `TestManagedSkillsLoadAndReadReachNextPayloadWithoutNativeDuplication`
  captures metadata, then the exact requested body, then its exact resource.
  Each v2 managed turn sends fresh bounded context, not native resumed history.
- `agentic_chat.go`: only typed host events persist delivery receipts.
  `TestManagedOptionalSkillRevocationStopsCachedDelivery` prevents stale trust.
- Both workflow paths now use each workflow's selection and carry previous
  results as task data. The previous selected body is not silently inherited.
- Final request checks include attachments and wrappers. Non-resumable chats
  no longer inject the same body into both system and message.
- FORGE review displays bodies, resource paths, digests and the baseline.
  Changed-after-review content and symlinked ancestors fail before approval.
  Installation uses the exact inspected snapshot, not a newly trusted reread.
- Terminal persona and pinned skill delivery use exclusively created temporary
  context files plus private per-session resource materialization. Existing
  files/conflicting sessions are refused; unchanged owned files are cleaned on
  exit/stop; user edits are preserved. Native CLI reads remain unknown.
- Agent packs carry immutable skill ZIPs and locks. A SHA-256-bound import
  review displays text resources and binary hashes; one confirmation approves
  only those digests and makes the agent immediately usable without per-chat
  activation. Pack content itself cannot carry host authority.
- The GUI now exposes one versioned library. Shipped built-ins are registered
  and approved when the user enables skills; legacy chat execution remains a
  compatibility boundary rather than a parallel authoring interface.

Run `bash scripts/test-skills.sh /absolute/path/to/PrAImate-FORGE-Codex-Kit`.
It runs root build/vet/tests, focused race tests, the separate GUI Go module,
all frontend tests and the frontend build. It installs no dependencies and
calls no model. The frontend suite includes library bridge tests; existing Vite
accessibility and large-chunk warnings remain.

The actual-kit test was run with
`PRAIMATE_FORGE_KIT=/home/parrot/Downloads/PrAImate-FORGE-Codex-Kit go test ./internal/core -run TestForgeKitLocalAcceptance -count=1 -v`.
Captured review digest:
`sha256:15595b5a9fe6ddc9c5dfa30d8f9cdf0542e870244238870c062080114148a26d`.
This proves delivery to a fake adapter, not model task quality.

Final rerun on 2026-09-09: `scripts/test-skills.sh` with the kit path above
exited 0, including root build/vet/full tests, focused race tests, GUI vet/full
tests, 35 frontend tests and Vite build. The initial sandboxed run could not
open local test servers; the authorized rerun passed. The headless fake-adapter
test now isolates native OpenClaude configuration in its temporary home as well
as isolating PrAImate data, so it does not modify the user's CLI profile.

See [test and rollback guide](skills-p8.md#testing-this-checkout) for the
Linux candidate, isolated profile and manual checklist.

The consolidated user checklist is now [SKILLS_TEST_GUIDE.md](SKILLS_TEST_GUIDE.md).
It covers all new controls and explicitly distinguishes unsupported paths.
Required dynamic bindings on static transports now fail; optional ones produce
diagnostics instead of advertising a nonexistent load/read broker. Managed
chats bypass that static builder and use their real broker.
