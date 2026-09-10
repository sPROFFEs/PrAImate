# P5 — Chat, Studio and workflow delivery

Status: **partial, controlled-path testing ready**. Pinned delivery is wired;
dynamic load/read is wired to managed runs, not every native/helper surface.
Installed-version pickers share backend lock construction in Chat and detached
Studio. Full authoring flows and real GUI/CLI behavioral acceptance remain
pending; see `skills-evaluation.md`.

## Actual execution paths

`ContinueChatStream` now repeats final resolution through
`BuildChatSkillPayload` immediately before launching the adapter. A fresh chat
places the controlled payload in the adapter-visible `SystemPrompt`; a resumed
native chat prepends a newly built payload to the current user request. This is
intentional: PrAImate does not claim that a CLI's private previous context is
still resident. The same chat path owns Document Studio (`SurfaceStudio`).

`runWorkflowSequence` freezes each workflow selection and builds its payload
for each user step using the actual task and shared host policy.
Resumed workflow turns likewise receive a fresh payload in their observable
request. At a v2 workflow boundary a new native session receives the next
selection and previous results as task data. The managed sequence also separates
workflow contexts. Required resolution failures occur before adapter calls.

Managed runs now share `managedSkillSession` between model and read-only broker.
Auto metadata becomes a body only after `skill.load`; `skill.read` contributes a
bounded resource range to the next fresh payload. Typed host delivery events
persist the receipt. Approval is revalidated before each outbound turn.

Only a sanitized `SkillRuntimeState` is retained in encrypted chat settings:
delivery status, coverage, measurement, epoch, ref/digest/kind and size. It
does not retain bodies, resources, prompt text, tool arguments, paths, secrets
or reasoning. Privacy notice v5 discloses this metadata. It replaces the latest
state rather than forming a diagnostic event log.

## UI

Chats show `prepared` versus `delivered`, coverage, epoch and controlled block
count. Document Studio shows the same observed state. Agent Studio continues to
use the strict P3 YAML preview and shared resolver. “Prepared” is not shown as
delivered; `controlled_payload` is not presented as native full-request control.

## Evidence

`TestChatDeliversControlledPayloadAndPersistsSanitizedReceipt` captures exact
body/digest on a fake adapter's first and resumed requests, then reopens the
chat and verifies sanitized state. `TestWorkflowUsesSameControlledPayloadPlanAsChat`
captures the same exact body/digest in a workflow fake adapter request. Frontend
tests verify the chat/studio evidence wording and all UI suites pass.

No CLI skills directory is created or modified in this phase. No global config,
AGENTS.md, CLAUDE.md, credentials, terminal process, package script or real model
is touched. P6 must characterize each adapter before native discovery or strict
selected-set claims are enabled.
