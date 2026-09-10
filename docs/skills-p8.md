# P8 — evaluation, rollout and rollback

## Local rollout gate

Controlled v2 skills are disabled by default. The encrypted local setting
`skills_v2_rollout` is exposed in the Skills tab and controls only v2
selection/delivery. Enabling it is an explicit per-installation cohort choice;
it neither enables legacy skills nor changes existing agent/chat locks.

When disabled, a v2 selection fails before adapter/model delivery with
`skills_v2_disabled`. It never falls back to legacy injection. Turning the
switch off preserves the immutable packages, locks, agent data and chat data;
turning it back on recovers the same pinned selection. Legacy chats and
terminal launches do not consult the v2 route and keep their existing behavior.

Legacy Terminal persona files are now temporary and exclusively created, never
overwritten. If the project already has native instruction files or another
agent terminal owns one, the launch reports the conflict. This safety correction
is not evidence of versioned native discovery.

The gate is enforced by Core for snapshots, default bindings, preview, chat,
workflow and FORGE installation. A frontend control is not itself evidence of
activation: the backend checks the persisted setting at each v2 entry point.

## Evaluation evidence

`docs/skills-evaluation.md` records deterministic and fake-adapter coverage,
with level-3 model evaluation explicitly marked `not_run`. No paid/model calls
were made. Costs, provider token counts and benchmark quality are consequently
`unknown`, not zero.

## Rollback demonstration

`TestSkillsV2RolloutRejectsWithoutFallbackAndReenablesPinnedChat` creates a
reviewed pinned v2 chat, disables the cohort, verifies the fake adapter sees no
request and the stored lock is unchanged, then re-enables and verifies the
exact digest reaches the controlled adapter payload. The separate legacy test
proves a legacy chat still reaches its adapter with no v2 payload while the
cohort remains disabled.

Native Terminal remains `unsupported` for controlled v2 context unless a
versioned adapter demonstrates scoped discovery. This rollout flag does not
weaken that boundary or claim a private native-context limit.

## Testing this checkout

Start with [the consolidated numbered test guide](SKILLS_TEST_GUIDE.md).
It includes the subsequent library, migration, task-budget and finish-gate
implementation; the narrower procedure below is retained for earlier fixtures.

This is a **test candidate, not the finished P0–P8 feature**. Read the
[acceptance audit](skills-evaluation.md) for implemented, partial and not_run
cases. Do not enable it by default or publish it based only on these tests.

### 1. Automated checks without a model

From the GitHub checkout, with the existing Go/frontend dependencies and Linux
GTK/WebKit development packages available:

```bash
cd /home/parrot/projects/praimate
bash scripts/test-skills.sh /home/parrot/Downloads/PrAImate-FORGE-Codex-Kit
```

The script stops on the first failed command. It runs root build/vet/tests,
focused race tests, GUI-module vet/tests, all frontend tests and Vite build.
It performs no dependency installation, commit, push, release or model call.
Tests use temporary stores and fake adapters. Network-policy tests use local
fixtures, not production endpoints. The kit argument enables the real-local-kit
import/transport test; omitting it leaves that test explicitly skipped.

The important transport regressions are in `internal/core/skills_runtime_test.go`:

- `TestManagedSkillsLoadAndReadReachNextPayloadWithoutNativeDuplication`:
  metadata first, exact body second, exact resource third; no native resume or
  permission escalation, and resource text stays out of the tool transcript.
- `TestWorkflowSequenceUsesEachSelectionAndPersistsLastLock` and
  `TestManagedWorkflowSequenceUsesEachSelectionAndPersistsReceipt`: next
  workflow receives its own selection and the previous task result.
- `TestInstalledSkillPickerBuildsExactLockWithoutGrantingTrust`: selected
  installed digest reaches the adapter; unapproved/foreign/duplicate selections
  fail, and selecting none remains explicitly empty.
- Chat attachment-budget, nonresumable-deduplication and optional-revocation tests.

`TestDetachedStudioVersionedSkillsUseScopedMainCore` covers the Studio window
bridge without opening a second Core or permitting another chat to be modified.
None of these is a claim that a real model followed the procedure.

### 2. Start the Linux test build in a clean profile

The review build is in:
`/home/parrot/Downloads/praimate-skills-test.2zJddn/`

It contains `praimate-gui` and the headless `praimate`, labelled
`1.2.5-skills-test` at link time. The source/release version remains 1.2.5.
This is an unpackaged local build, not an installer or published artifact.

```bash
test_profile=$(mktemp -d /tmp/praimate-skills-profile.XXXXXX)
printf 'Keep this profile path for subsequent tests: %s\n' "$test_profile"
PRAIMATE_HOME="$test_profile" \
  /home/parrot/Downloads/praimate-skills-test.2zJddn/praimate-gui
```

Set a temporary database password in the GUI; leave Remember disabled. Accept
the privacy notice and choose a disposable projects folder. Skip backup, sample
installation and CLI installation during this smoke test.

`PRAIMATE_HOME` isolates PrAImate-owned data, **not the native CLIs' home
configuration, credentials, hooks, caches or project access**. For a fully
isolated behavioral test use a disposable VM/account and project. Never select
your production checkout for an agent that may write. Do not use Delete all
data as a test-cleanup shortcut on a live profile.

### 3. Review and import the local kit

1. Open Skills and explicitly enable **Controlled v2 skills**.
2. Agents → **Import…** → choose the external FORGE `.praimate-agent` package.
3. Inspect the baseline, each skill's body, digest and preserved-resource list.
   Review resource contents in the source directory too. Installation does not
   execute resources or turn their permissions into host authority.
4. Check the exact-version authorization and select **Authorize and install
   reviewed snapshot**. Expected: nine versions and the separate `forge-dev-v2`
   agent. Existing baseline agents are not replaced.
5. To test changed-after-review rejection, use a disposable copy of the kit:
   inspect it, edit a referenced resource, then approve the old preview.
   Expected: `forge_review_changed`, no new approval or version installed.
   Cancel and inspect again to approve the new content deliberately.

### 4. Test pinned skills in Chat and Studio

Only run a model when you have deliberately selected an authorized endpoint or
account. The automated test above needs neither. Real calls may incur cost.

1. Create a **new**, unsent Chat with the test agent (or a clean chat). Choose
   a working folder and CLI/model. Keep tools read-only/safe for the first test.
2. Open **Skills**. Select one installed `local/forge-review` version with
   **Always include**, then choose **Save skills**. Lock construction,
   validation and persistence happen in that single action.
3. Send: “Review the selected small project without editing files or running
   scripts. Report confirmed findings and what you could not verify.”
4. Check the delivery status and `coverage=controlled_payload`. The preview is
   only eligibility; a checkbox or a model saying “I used it” is not evidence.
   Exact body delivery is pinned by the automated capture tests above.
5. Send a follow-up, navigate away and reopen. Check the same saved selection
   and latest receipt. A native resumed CLI may retain/repeat earlier context;
   the receipt covers PrAImate's new payload, not its private history.
6. Repeat from a new Studio session. Its chat pane exposes the same versioned
   picker and scoped backend. Save before the first turn; binding changes in a
   started native session require a new session. Do not modify bindings while
   a turn is running.
7. For explicit none, create another new session, uncheck every version and
   Build/Save. Expected: an empty v2 selection, not inherited defaults.

The ordinary Skills Add/URL/ZIP catalogue is still **legacy**. It is not the
versioned package importer. The installed-version picker does not publish,
update or approve arbitrary packages; end-to-end authoring is still pending.

### 5. Workflows and progressive loading

FORGE's initial agent selection is intentionally empty; its individual
workflows select their skills. Run **Revisar** for the simplest pinned trial.
Use read-only permissions and a disposable project. Switching workflow must
not carry the previous workflow's selected body into the next run.

Progressive `skill.load` / `skill.read` is wired to the **managed single-agent
runtime** (Autonomous / `mode=agentic`). The offline three-turn capture test is
the deterministic first test. For a real trial, use a separate managed agent
with an approved auto binding; require JSON tool actions and inspect the
host delivery receipt. Native/authoring-helper chats do not have this broker:
use pinned bindings there. No automatic delegation or privileged skill.run is
implemented. A reported final answer does not satisfy an evidence-quality gate.

The corresponding headless workflow, after importing in the same test profile,
can be run deliberately with:

```bash
PRAIMATE_HOME="$test_profile" \
  /home/parrot/Downloads/praimate-skills-test.2zJddn/praimate agent run \
  --agent forge-dev-v2 --cli praimate-code \
  --endpoint saved --model YOUR_LOADED_MODEL \
  --folder /absolute/path/to/disposable-project \
  --workflow Revisar --input objetivo='Read-only review; no scripts or edits' \
  --output json
```

Configure the saved local endpoint in the test GUI first, or omit endpoint/model
to use an intentionally authorized CLI account. Do not put the DB password or
API key in argv; use the hidden terminal password prompt. See
[CLI agent API](CLI_AGENT_API.md) for the existing output/error contract.

### 6. Rollback and Terminal regression check

Disable v2 in Skills and retry the saved v2 chat: expect `skills_v2_disabled`
before adapter delivery, not silent legacy fallback. A clean legacy chat should
still work. Re-enable and reopen the v2 chat: the same locked version remains.
Finish/stop active runs before toggling or closing windows.

For Terminal, use a **legacy** agent with a clean disposable project. Check that
its temporary AGENTS.md/CLAUDE.md contains its persona, and disappears after
Stop/exit. An existing user file or conflicting live session must be preserved
and the new launch refused. Editing the temporary file preserves it at cleanup;
remove it yourself only after review. A process crash can leave the file behind;
it is not automatically overwritten on the next launch. Native v2 skills still
fail with an unsupported/incompatible-transport diagnostic on built-in adapters.

### Record a test result

Record OS, test-build path, CLI version, model ID, surface/runtime mode, selected
ref/digest, operation, actual result, error and receipt coverage. Do not include
keys, passwords, private prompts or raw model traffic in reports. Separate
transport success from task quality, and record skipped cases as not_run.
