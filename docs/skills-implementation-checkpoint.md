# Skills implementation checkpoint — 2026-09-09

Checkout: /home/parrot/projects/praimate, main, HEAD
d60480482c257ad0f79d561c5657d32784aec66e plus substantial uncommitted work.
Preserve unrelated scripts, AGENTS.md and all prior changes. No commit, push,
release, global installation, real model call or user-data migration performed.

## Current increment

Implemented the host-only versioned library command service and GUI:
directory/ZIP/GitHub inspection, explicit selection and shared associations,
review-digest guarded installation, immutable drafts/publication, explicit
forks, own-version editing, exact approval/revocation, package export, and
reviewed idempotent built-in/legacy catalogue copying. Draft revisions protect
against stale saves even after refreshing the library. Imported permissions
never become host authority. Detached windows cannot manage the library.

Cumulative controlled-input accounting now includes prepared skill payloads,
is reserved before transport, and persists both run counters and atomic task
counters in the encrypted DB. Chat continuation, workflow subruns and durable
CLI retries share task identity; resume cannot reset/widen its ceiling.
Measurements are bytes, not provider tokens/cost/private context.

V2 workflow finish_evidence supports required current-run artifacts, minimum
size and optional exact SHA-256. Requirements persist across resume.
Missing/mismatching artifacts produce quality_gate_unmet; native workflows
reject this unverifiable contract before launch. This does not validate
command results against a Git revision or certify code quality.

Static chat/helper transports now reject required dynamic bindings and
diagnose omitted optional ones. Managed chat bypasses the static builder and
uses its actual skill.load/read broker. Existing pinned delivery, exact locks,
scoped Studio RPC and rollback remain covered.

## Verification

Final command passed:
bash scripts/test-skills.sh /home/parrot/Downloads/PrAImate-FORGE-Codex-Kit

Root build/vet/all tests, agentic race tests, focused Core/skills race tests,
GUI vet/all tests, 36 frontend tests and Vite build passed. Existing Vite
accessibility/large-chunk warnings remain. git diff --check passed.
Linux GUI and CLI test builds are in
/home/parrot/Downloads/praimate-skills-test.2zJddn, label 1.2.5-skills-test.
No source version bump. Actual GUI/model/Windows behavior remains not_run.

## Files and user test entry point

- internal/core/skill_library*.go; internal/skills/host_read.go,
  version_content.go, legacy_migration.go: library lifecycle.
- cmd/praimate-gui/skill_library.go; frontend/src/lib/SkillLibrary.svelte,
  api.js, skillBindings.test.js; pages/Skills.svelte: library controls.
- internal/agentic/{types,runtime,budget_test,evidence,evidence_test}.go:
  prepared-input accounting and artifact finish gate.
- internal/core/skill_task_budget*.go, agentic*.go, agent_runtime.go,
  agent.go, agent_yaml.go, workflow_runner.go, workflow_evidence_test.go,
  chat_interactive.go, skills_runtime*.go; cmd/praimate/agents_cli.go:
  integration, durable scope and transport gates.
- frontend/src/pages/AgentStudio.svelte: visible run counters/verdict scope.
- scripts/test-skills.sh, README.md, docs/SKILLS_TEST_GUIDE.md,
  docs/skills-evaluation.md, docs/SKILLS_TEST_GUIDE.md: checks and consolidated guide.

## Outstanding implementation and validation

The skills implementation is NOT universally complete. Remaining: progressive loading on
non-managed surfaces; version-characterized production native discovery or
explicit verified fallback; fully guided agent/workflow binding authoring
without definition editing; broader evidence-verifier contracts; actual
Ponytail/Matt revisions/license acceptance and paired model evaluation.
No pending item should be reported as acceptance-passed.

Next safe work: inspect docs/05-SURFACES-ADAPTERS-UX.md and close non-managed/
native transport and guided-authoring gaps with payload/fake-process tests.
Real model calls still require authorization. User manual testing starts at
docs/SKILLS_TEST_GUIDE.md is the canonical test and rollback guide.
