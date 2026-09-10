# P7 — FORGE and external compatibility

FORGE is the first v2 consumer, not a special product path. Acceptance tooling
converts its reviewed local design kit into a normal `.praimate-agent` package;
the GUI only exposes the generic reviewed agent importer. It does not fetch
external candidates, execute resources, create a commit, or publish anything.

## Explicit local trust

`core.InstallForgeOwnSkills` requires both `reviewedTrust=true` and the exact
combined digest returned by `InspectForgeKit` before opening a transaction.
It validates the kit root/index,
inspects each package through the normal bounded importer, creates immutable
`own:forge-*` identities, approves exactly those reviewed digests, and exports
a lock from the installed versions. The target lock example is not copied as
authority.

`Core.InstallForgeAgent` then reads the v1 baseline and creates the separate
`forge-dev-v2` agent. Each workflow has one pinned selected skill and a
workflow-specific lock. Its generated workflow prompt contains task inputs and
the policy reference only; it does not duplicate the procedure/checklist from
the selected skill. A task without a selected binding still has no activation.
The baseline is parsed and its workflow mapping is validated before any host
approval, so an incompatible kit cannot leave the local skill host partially
installed.

In the Agents tab, **Import…** accepts the resulting external agent package and
displays its agent definition, skill bodies, digests and resource inventory.
The explicit approval applies to that preview digest. The generic backend
verifies the package again, installs and approves its exact bundled versions,
and retains its bindings. Changed content fails before approval. Cancelling the
review changes no host approval.

The standard Terminal stays outside this path unless its adapter has verified
scoped native delivery. FORGE's controlled chat/studio/workflow route remains
the supported first trial.

## External sources and forks

Ponytail and Matt candidates remain unresolved candidates: their supplied kit
records have no fixed revision, so P7 does not fabricate a lock or download
them. A reviewed external import must use `SourceProvenance` with a canonical
HTTPS origin and a full 40-character commit. The original retains its external
identity and license resource. A compatibility adaptation uses `Fork`, which
creates a new own source/digest and records `derived_source` plus
`derived_digest`; it never mutates or re-labels upstream.

`ReviewExternalSkill` is a conservative local inspection helper. It marks
subagent/delegation language as requiring an explicit fork when PrAImate cannot
produce independent delegation evidence, and reports probable entrypoint
dependencies. It does not install dependencies, call the network, or claim a
Markdown mention is a complete dependency graph.

The advanced bindings preview shows `source_kind` and an explicit
`derived_source@derived_digest` label for forks. A selected external original
and its fork therefore remain distinguishable in both lock identity and UI.

## Acceptance evidence

`internal/core/forge_test.go` proves that unreviewed local trust leaves the
host untouched; the nine local skills generate pinned reviewed identities and a
lock without external services; FORGE persists as a v2 agent with one selected
workflow skill; Review sends the pinned review body to a fake controlled
adapter without workspace writes; and an external original/fork preserves its
license and distinct provenance while delegation is blocked pending a fork. It
also proves that a baseline with an unmapped workflow produces no host approval.
