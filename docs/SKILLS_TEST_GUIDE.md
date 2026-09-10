# How to test skills in PrAImate

Use this guide from top to bottom. It covers the new library, authoring,
selection, reviewed agent packs, Chat, Studio, Terminal, workflows, managed
tools, budgets, portability and rollback. Keep a copy of the checklist with
your test results.

This is an uncommitted Linux test build, not a published release. Automated
checks use fake adapters. You run the real-model checks deliberately, with
your own endpoint and permissions. Do not use a production project.

## 1. Start safely

Close other test copies of PrAImate. On Linux:

```bash
test_profile=$(mktemp -d /tmp/praimate-skills-profile.XXXXXX)
printf 'Test profile: %s\n' "$test_profile"
PRAIMATE_HOME="$test_profile" /tmp/praimate-skills-ux/praimate-gui
```

Keep that path. Reuse it to reopen the same test database. Set a temporary
database password, leave Remember disabled, skip backup and automatic
installations, and select a disposable projects folder. Configure your local
endpoint/model only if you intend to run model tests.

On Windows, use a separately compiled Windows test executable, not the Linux
binary. Set `PRAIMATE_HOME` to a new directory in PowerShell before launching
it. Windows execution has not been verified by the Linux test suite.

`PRAIMATE_HOME` isolates PrAImate data, **not native CLI configuration,
credentials, hooks, project files or caches**. A disposable VM/account is the
appropriate isolation for mutation-capable CLI tests. Do not test Delete all
data against your normal profile.

## 2. What the states mean

| State/action | What it proves |
|---|---|
| Inspect | Source parsed; nothing installed or executed |
| Install / publish | Immutable package registered; not automatically approved or selected |
| Approve exact version | Host permits selection of that source/digest; no command permissions granted |
| Select / preview | Preferences resolved against locks and trust; not delivery |
| Prepared / pending | Host built context; delivery not yet confirmed |
| Delivered | Content reached the controlled adapter payload; not proof the model followed it |
| Resource read | Host broker supplied a bounded resource range |
| Evidence verified | Explicit artifact presence, size and optional hash checks passed; not a general code-quality verdict |

## 3. First visible result: create and publish your own skill

1. Skills → **Enable skills**. The shipped built-ins appear in the same
   library as local and external packages.
2. Under Authoring, enter `local/test-review`, then **Create draft**.
3. Replace `SKILL.md` in the editor with:

```markdown
---
name: test-review
description: Use for a short read-only review of a small code sample.
---

Review only the supplied code. Do not edit files or execute commands.
List a reproducible issue or state that none was found. Separate facts
from assumptions. End the response with TEST-REVIEW-A.
```

Click **Save draft**, then **Review publication**, then
**Publish (approval still required)**.

Expected: a new installed version and digest appear. It is not approved.
Reopen the draft after navigating away: your text remains saved. No model call
is necessary for this test.

## 4. Review and approve

Select **Open details** for `local/test-review`. Inspect the full entrypoint
and every resource. Tick the explicit review confirmation and click
**Approve exact version**.

Expected: approval changes for this exact digest only. No session starts and
no global default changes. Exported packages will not contain this approval.

## 5. Test Chat delivery and reopening

Create a fresh Chat. Open its **Skills** selector. Select `local/test-review`,
choose **Always include**, then **Save skills**. The UI builds and validates
the exact immutable lock as part of this single action.

The PrAImate **Skills → Installed** page is the host library. It is separate
from the skill catalogue shown by a native CLI. A skill can be delivered by
PrAImate without appearing in the CLI's `/skills` or equivalent command: Chat
adapters receive the approved body in the controlled request payload, while
the CLI's own global skill directory is not modified. Conversely, a skill
loaded only by a CLI or copied into a project is not automatically a PrAImate
skill and has no PrAImate approval or delivery receipt.

Send a tiny read-only task, for example:

```text
Review this Python function without executing it:
def first(items):
    return items[0]
```

Expected:

- The saved selector retains the exact digest after closing and reopening.
- Before transport, the state does not falsely say delivered.
- After successful transport, the controlled-payload receipt lists the digest.
- The reply may contain TEST-REVIEW-A. This marker tests behavior only;
  the receipt/fake-adapter capture is the delivery evidence.
- A follow-up uses the saved selection. Native CLI private history is not
  inspected and is not included in a claimed full-request budget.

After the first turn, open the chat settings and check the runtime line. The
meaningful evidence is `delivered · controlled_payload` plus the listed
controlled block count. `prepared — pending delivery` means the host resolved
the skill but has not yet confirmed transport; no runtime line means that
turn did not deliver a PrAImate skill. Model text claiming “I used Ponytail”
is not delivery evidence.

Run the same checks with a Chat opened from an agent. An existing chat keeps
its frozen selection when the agent definition is later edited.

## 6. Explicitly choose no skills

In a fresh session, open **Skills**, uncheck every version and choose **Save
skills**. Do not merely close the panel.

Expected: no inherited defaults reappear and no skill body is delivered. A
task that needs no skill is a valid result, not a failure to activate something.

Changing the lock of a session with retained native state may require a new
session. A resumed native chat cannot replace its private system prompt, so
PrAImate prepends a newly selected pinned payload to the resumed request; for
the clearest test, create a new chat after changing skills. Do not expect
clearing a checkbox to erase private CLI history.

## 7. Publish version B without changing version A

Review version A → **Edit as new version**. Replace TEST-REVIEW-A with
TEST-REVIEW-B, save, validate and inspect the side-by-side changes. Publish.

Expected:

- B has a new digest; A remains readable and unchanged.
- Existing chats still pin A.
- B does not inherit approval. Approve B separately before selecting it in
  a new chat.
- LICENSE files, binary resources and executable intent are preserved when
  editing; selecting a binary file does not convert it into text.
- Two editors cannot overwrite the same draft with stale revisions. If a
  revision conflict appears, reopen the draft and reconcile your changes.

## 8. Import a collection and its resources

Use a test directory or ZIP containing two different subdirectories, each
with a `SKILL.md`. Place a `LICENSE` at collection root and a small resource
such as `references/example.md` in one package. Do not use sensitive data.

In Skills → **Import**:

1. Select Local folder or ZIP archive and browse to its path.
2. **Review source**: expect two candidates, not concatenated instructions.
3. Choose only one; assign a unique `namespace/name`.
4. Explicitly include the shared LICENSE if needed. Shared paths are copied
   to the displayed relative destination; imports do not rewrite references.
5. **Review selected packages**. Expand entrypoint, resource and license files.
6. Confirm the reviewed content and install.

Expected: only the selected package is installed, with its reviewed files and
license. Scripts are never executed by inspection, installation or approval.
Repeating the same source/identity/content does not replace another version.
Two unrelated sources trying to use the same ref produce a collision error.

For the changed-source check, modify the local entrypoint after final review
but before Install. Expected: review-mismatch error; inspect again. The backend
never installs unreviewed changed bytes under the previous review.

The interactive library limits expanded content to 8 MiB and 1,000 entries.
Other package services retain their own limits. Oversized input is rejected,
not silently truncated.

## 9. Optional external import

Select GitHub repository, enter its root URL, and supply branch/tag/commit
and subpath in separate fields. Leave ref empty to resolve the default branch.
Inspection downloads source and replaces the ref with a full commit hash.
Review only the desired candidates and explicitly include required licenses.

Do not put credentials in the URL. Private-network sources and ambiguous
`/tree/branch/path` URLs are not accepted by this GUI source adapter.
This test uses the network, but does not call a model or run repository code.
Actual Ponytail/Matt acceptance remains separate from a synthetic import test.

## 10. Fork imported or built-in content

Review an imported version, enter a new identity such as `local/review-fork`,
then **Fork to draft**. Modify, save, preview and publish.

Expected: the original is unchanged. The fork retains resources and licenses,
has its own source/digest, and shows `derived_source` and `derived_digest`.
It requires separate approval. Imported/built-in content cannot be edited
under a false claim that the modified version is the original.

## 11. Legacy compatibility boundary

There is no second legacy catalogue or legacy picker in the GUI. Existing
legacy chats continue to execute their saved configuration for rollback and
backward compatibility, but new authoring, imports and selections use the
versioned library. Do not expect old inline skills to appear as editable
versioned packages automatically.

## 12. Export and agent-pack portability

Review a version → **Export package ZIP** → choose a new filename. Existing
files are not overwritten. Inspect the archive using an archive viewer.

Expected: entrypoint/resources/licenses only, no DB, host approvals, sessions
or credential envelope. Content you authored may itself contain secrets;
inspect before sharing.

Also export a v2 agent pack using the agent's existing export action. Import
it into a second test profile. The import dialog must show every bundled ref,
digest, entrypoint/resource body or binary hash, plus executable intent. Tick
the single package-review confirmation and import.

Expected: YAML, workflow modes, locks, resources and licenses survive. The
single reviewed operation installs and approves only the exact bundled
digests, enables the shared skills system locally, and retains the agent bindings.
Opening a new Chat or Studio from the imported agent needs no second skill
approval or activation step. Changing the pack after preview must fail with
`agent_pack_review_changed`. A pack cannot carry host approvals in its own
files. Bare legacy v1 YAML remains importable but carries no skill bundles.

## 13. Chat, Studio and Terminal automatic loading

Create a document Studio session, then repeat the pinned Chat test. Repeat
once by opening Studio from an agent. Close and reopen the session.

Expected: selector, saved lock and controlled receipt remain consistent.
The detached window uses the main process's scoped backend; it does not open
a second database or gain library-edit/trust access for unrelated sessions.
Authoring-helper Studio remains a different, static CLI path; use pinned there.

Verify normal editing, stop, window closure and reopening still work. Never
infer delivery merely because a detached window loaded successfully.

Then open a Terminal directly from the same imported agent in a disposable
project that has no existing `AGENTS.md`/`CLAUDE.md`. Expected: PrAImate
creates an exclusively owned temporary context file containing the exact
pinned skill bodies and exposes package resources through
`PRAIMATE_SKILL_MANIFEST`; Stop/exit removes unchanged temporary files and the
private materialization. Existing project instructions cause an explicit
conflict instead of being overwritten.

Terminal delivery is deliberately labelled compatible/unknown: PrAImate can
prove what it materialized, but native CLI private prompt assembly does not
prove that the CLI read or followed it. Auto/manual dynamic bindings still
require the managed broker; portable agent packs should use pinned bindings
for behavior required on every surface.

### FORGE acceptance

1. Agents → **Import…** and choose the external FORGE `.praimate-agent` file.
   FORGE has no dedicated installer or privileged import path.
2. Confirm that the review lists all nine bundled FORGE skills and their exact
   resources, then approve **Import agent and skills**.
3. Confirm FORGE appears in Agents and its skills appear under
   Skills → Installed as Ready.
4. Open a plain Chat, Studio and Terminal from FORGE without editing skills.
5. Run `Revisar` and `Desarrollar`; inspect controlled receipts.

Expected: plain surfaces load the pinned context, simplicity and verification
skills. Workflows use their locked specialist skill (`Desarrollar` uses TDD),
so every FORGE package is carried by the exported agent without hard-coding a
FORGE-only import path.

## 14. Managed dynamic loading and resource reads

Use an explicitly **managed / agentic** agent, not a native agent. In its
session selector choose an approved package in **auto** mode. Give it a small
task that needs that package and one of its text resources.

Expected first request: bounded catalogue metadata, not every body. The host
broker accepts only the eligible exact ref/digest. A successful `skill.load`
prepares the body for the next controlled request; `skill.read` prepares a
bounded text range. Repeated resident loads/ranges do not duplicate the body.

The model-facing operations are:

```json
{"action":"tool","tool":"skill.load","arguments":{"ref":"local/example","digest":"sha256:THE_ACTUAL_LOCKED_DIGEST"}}
{"action":"tool","tool":"skill.read","arguments":{"ref":"local/example","digest":"sha256:THE_ACTUAL_LOCKED_DIGEST","path":"references/example.md","start":1,"lines":40}}
```

Replace the example identity with the real lock. **manual** excludes catalogue
advertising but allows an explicit load of an eligible known ref. **off** does
neither. Neither mode is a permission to execute a script.

Static/native Chat and authoring-helper paths do not provide this broker.
Required auto/manual bindings fail explicitly there; optional ones are omitted
with diagnostics. Use pinned or choose a managed agent deliberately. There is
no hidden runtime switch and no fake dynamic-loading claim.

## 15. Budgets, retries and revocation

In a disposable managed agent's `runtime.json`, set
`limits.max_total_input_bytes` to `100`. Run a prompt.

Expected: failure/stall before model delivery because the required request is
already too large. Restore a sensible value for new tasks. Default cumulative
managed limit: 8,388,608 controlled-input bytes.

For a longer test, set a small viable limit, run several turns, interrupt and
resume. Input reservations include skill wrappers and are saved before
transport. Failed calls count. Resume cannot reset or raise the original task
ceiling. Durable headless retries using the same `--run-id` share the task
counter, as do workflow subruns. Plain v2 chat uses an 8 MiB conversation
counter. Starting a genuinely new task is distinct from retrying the old one.

Managed-run details show byte counters and their coverage. These are not exact
token counts, provider billing, or a guarantee on private native CLI context.

Separately, reduce the per-payload skill budget below a required body's size.
Expected: no partial body and no model request. Restore the original budget.
Revoke a selected version in the library: the next controlled request must
reject/diagnose it, including cached optional versions in active managed runs.

## 16. Require an artifact before workflow completion

In a **v2** agent's YAML, add this field to a workflow and use an explicitly
managed agent runtime:

```yaml
finish_evidence:
  - artifact: report.md
    min_bytes: 20
```

Ask the workflow to produce the report with the managed `artifact.write` tool.
Expected: `finish` cannot succeed before that nonempty artifact exists in this
run and meets the size requirement. Add `sha256` with a known lowercase,
64-character digest if the task requires exact expected bytes.

Host errors use `quality_gate_unmet`. Requirements survive resume even if a
caller omits them. Native workflows reject the requirement before launching.
The verifier does **not** certify tests, a Git revision, review coverage or
the correctness of statements inside report.md. Those require separate
verifier contracts; they are not implemented by this artifact check.

## 17. FORGE workflows

Agents → **Import…**, choose the external FORGE `.praimate-agent`, review its
agent definition, skill bodies, resources and digests, then confirm import.
Expected fixture: nine own skills and seven workflows. This is the same reviewed
agent-pack path used for every third-party or locally exported agent.

Test Revisar, Desarrollar, Preparar deploy and a trivial documentary task in a
disposable project. Inspect the workflow's actual selection/lock, not just its
name. Give each task the least permissions it needs.

- Revisar: reproducible findings or clear limits; no unapproved writes.
- Desarrollar: bounded change and current test evidence; no implicit commit.
- Preparar deploy: preparation only; no publication or deployment.
- Trivial task: zero unnecessary activations is acceptable.

Changing workflow must use its own lock; previous workflow results may be
carried as task data, but its selected skill body must not be inherited.

## 18. Headless API

After configuring the same test profile and saved local endpoint:

```bash
PRAIMATE_HOME="$test_profile" /home/parrot/Downloads/praimate-skills-test.2zJddn/praimate agent run \
  --agent forge-dev-v2 --cli praimate-code \
  --endpoint saved --model YOUR_LOADED_MODEL \
  --folder /absolute/path/to/disposable-project \
  --workflow Revisar --input objetivo='Read-only review; no scripts or edits' \
  --tools safe --output json --run-id skills-review-test
```

Replace model/project values. Use the hidden password prompt; do not place
the DB password or API key in command arguments. Save stdout and check that
it contains the existing versioned JSON result, not mixed diagnostics. Use
`--output jsonl` for events. A repeated completed ID replays its result; an
explicit `--retry` is a retry, not a fresh budget.

Compare equivalent GUI/headless selections and failures. See
[CLI agent API](CLI_AGENT_API.md) for Python integration, status and permissions.

## 19. Terminal boundary and legacy regression

Production v2 Terminal discovery is still **unsupported** until an adapter's
scoped discovery is characterized. Expect an explicit preflight error, not a
frozen window, empty session, fallback, or a green “used” indicator.

For a legacy agent, test Terminal startup, Stop, detach, window close and
reopen. Temporary persona context must not overwrite an existing AGENTS.md or
CLAUDE.md. A conflict should give an error; test in a clean project instead.
Unchanged owned files are removed on normal exit; user edits are preserved.
After a crash, inspect any leftover file before manually removing it.

## 20. Rollback

Disable v2 in Skills. Expected: legacy sessions still work; stored v2 sessions
fail explicitly with `skills_v2_disabled`. No packages/locks are deleted and
no v2 selection silently becomes legacy. Re-enable and repeat the same v2 task:
the original digest should still be selected.

## 21. Automated suite and result sheet

```bash
cd /home/parrot/projects/praimate
bash scripts/test-skills.sh /home/parrot/Downloads/PrAImate-FORGE-Codex-Kit
```

This runs root build/vet/tests, agentic and skills race tests, separate GUI
tests, frontend tests and build. It installs nothing and calls no model.

For every manual section above, record:

| Field | Your result |
|---|---|
| Test section and date | |
| App build / OS / CLI version | |
| Model / runtime mode | |
| Skill ref and digest | |
| Expected vs actual result | |
| Receipt/event or reproducible error | |
| PASS / FAIL / NOT RUN / UNSUPPORTED | |

Do not include passwords, API keys, private corpus text or full prompt logs.
Report the **first** failure with its section number and exact error.

## Troubleshooting

| Symptom | Action |
|---|---|
| No Versioned library tab | Enable v2 in Skills first |
| Not approved / untrusted source | Review and approve the exact installed digest |
| Review changed | Reinspect the source/selection; never reuse an old approval |
| Host or draft revision conflict | Reopen and reconcile; another operation changed the snapshot |
| Required auto/manual cannot run | Use pinned or an explicitly managed runtime |
| Cumulative budget exceeded | Old attempts remain charged; do not retry expecting a reset |
| quality_gate_unmet | Produce the required current artifact; inspect size/hash contract |
| native_skill_unsupported | Verified native discovery is not available; use a supported controlled path |
| Model replies incorrectly despite delivery | Record a behavior failure; a receipt proves delivery, not compliance |

## Current implementation limits

Do not call this universal native support or a completed behavioral evaluation.
Dynamic tools on non-managed surfaces, real native adapter characterization,
actual external-source acceptance and Windows/real-model validation remain
outstanding. Per-run byte accounting does not account for hidden provider
usage or certify monetary limits. See the maintained
[acceptance record](skills-evaluation.md) for the complete outstanding list.
