# P4 — controlled runtime context

Status: **partial; reviewed runtime foundation**. See `skills-evaluation.md`
for the authoritative acceptance audit. Cumulative task/retry budgets and
evidence-required finish remain unimplemented. Configuration/model claims are
not delivery evidence.

`internal/skills/runtime.go` provides one per-payload `Runtime`. It accepts only a P3-resolved, host-approved `controlled` selection and a host-built `ContextBudget`. It reads immutable versions through `HostSkillStore`, never a model path, digest, epoch or policy. `internal/core/skills_runtime.go` owns the store while a `ControlledRuntime` lives.

`Build` emits bounded metadata for auto bindings and exact bodies for pinned bindings. `Load(ref,digest)` accepts only eligible exact versions, resolves declared `praimate.dependencies` inside already eligible bindings and commits the closure atomically. `Read` requires a resident body, rejects absolute/backslash/traversal paths and binary data, and returns a bounded line cursor. Imported `allowed-tools` and requirements never reach a command broker.

Blocks record ref, digest, type, size, activation and context epoch. Receipts use only `controlled_payload`, `full_request` or `unknown`, with tokenizer/estimate/bytes measurement. Core uses `controlled_payload`; no native CLI is called full-request controlled. `Compact` clears residence and advances epoch. New runtimes have no residency.

Budgets enforce available input after window/reserved output/safety/non-skill input and the catalogue/body/resource/total/max-active/load/read/bytes limits. Failed closures restore context reservations; verified byte cache is not delivery state.

## Acceptance evidence

| Criterion | Evidence |
| --- | --- |
| CTX-01 | `TestControlledSkillRuntimeCapturesExactPinnedPayload` captures exact body/digest. |
| CTX-02 | `TestRuntimeAutoCatalogueIsBoundedAndNotBodies`. |
| CTX-03–05 | `TestRuntimeBuildLoadReadResidenceAndCompact`; runtime is per payload. |
| CTX-06, CTX-10 | budget/atomic/concurrency tests. |
| CTX-11 | Not implemented: cumulative accounting across attempts/restarts. |
| CTX-12 | Estimate/coverage recheck; absent tokenizer rejected, not called exact. |
| CTX-07 | `TestRuntimeDependencyClosureAndCycleAreAtomic`. |
| CTX-08–09 | bounded read/traversal tests. |
| CTX-13 | receipt coverage tests; no invented full-request coverage. |
| SEC-01–02 | host-only budget/trust and exact-digest reader checks; no execution endpoint exists. |

CTX-14 is not asserted: the current workflow schema has no evidence-required finish declaration. P5/P7 must add a validated criterion before `quality_gate_unmet` is meaningful. Model prose is never evidence.

Passed: root build/vet/full tests; GUI Go tests; 30 frontend Node tests and Vite build; P4 runtime race tests; Windows amd64 Core/skills test compile. Native Windows execution, interactive Wails QA and real model calls are `not_run`.

No skill script was run, no network source contacted, no commit/push/release created.
