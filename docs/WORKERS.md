# Workers

Workers coordinates three independently configured model profiles: **Reasoner**
(`primary`), **Middle** and **Fast**. The feature is available in the Desktop
Workers page and the Studio / VS Code extension's **PrAImate: Workers** view.

## Parallel task graphs

Select **Parallel task graph** when creating a worker chat. The workspace must
be a clean Git repository root on a checked-out branch with an initial commit.
The Reasoner returns a draft DAG; it does not modify the project while planning.
Review task descriptions, dependencies, profile, CLI/local endpoint and model
before pressing **Run task graph**. Routes reuse the existing backend/model
registry. A task override takes precedence over its selected profile; provider
routing follows that backend's existing model ID or configured local endpoint.

The host schedules independent tasks concurrently (default 2, maximum 4 per
run). Every task receives a separate Git worktree and scoped context. Its
worktree lives in PrAImate's dedicated user-cache directory outside the project,
so CLI project discovery sees a normal checkout. Its
transitive dependencies are cherry-picked into that worktree before execution.
Workers use the existing JSON tool protocol and profile permissions; delegation
inside a DAG task is disabled. The host creates a single task commit and its
diff, including any model-created commits, rather than trusting a worker's
claimed changes. A no-change task has a saved result without a new commit.

Task cards preserve profile, CLI, provider/model route, branch, worktree path,
base/result commits, changed files, result and Git diff. Diff previews are capped
at 256 KiB; the worktree and saved commits provide the complete patch. Live
events include task IDs in the existing worker panes.

Accept dependencies before accepting their dependent results. **Merge accepted
changes** cherry-picks the accepted task commits, in dependency order, into a
separate review worktree based on the current target branch. Only a successful
integration fast-forwards the target branch. A conflict stops there and reports
the review worktree path; the original checkout remains unchanged. No automatic
conflict resolution runs. Return to the recorded target branch with a clean
workspace before merging. Inspect a failed merge and use **Clean reviewed
worktrees** before retrying it.

Merged worktrees are removed after integration. Rejected worktrees can be cleaned
explicitly; saved diffs and private Git result refs preserve review history.
Deleting a worker chat removes its temporary worktrees and result refs, including
unmerged work, after a frontend confirmation. Interrupted tasks never restart
automatically: inspect their worktrees, then explicitly **Discard failed work
and reset** before resuming pending tasks. Completed descendants prevent resetting
their dependencies. Task worktrees remain pinned to the original planning base.
Pending tasks can resume after this run merges its accepted results, including
after an app restart. Unrelated workspace HEAD changes require a new plan.

Worktrees separate checkouts, not operating-system permissions. They share Git
metadata, and commands still require the existing host approvals. Each task has
the existing per-worker turn/request/time bounds. The graph supports up to 32
tasks; concurrent run limits also remain in place. Plans, task states, activity
and review results use the same encrypted local chat storage as Workers.
Task IDs use lowercase letters, numbers, underscores and hyphens (up to 64
characters); `review-merge` is reserved for the host. Graph checkpoints replace
the current stored snapshot instead of duplicating all diffs. Retained graph
activity is capped at 500 events and 1 MiB of event text.

The same operations are available in VS Code and the maintenance CLI:

```sh
praimate workers plan --config workers.json --workspace /path/to/repo \
  --task "Implement the feature and its tests" --parallel 2
praimate workers show --id RUN_ID
# Optionally edit a tasks JSON array and pass --plan tasks.json.
praimate workers execute --id RUN_ID
praimate workers review --id RUN_ID --task-id task-a --decision accepted
praimate workers merge --id RUN_ID
```

`workers.json` is the existing `Config` format with `workspace` and the three
`profiles`; saved defaults are used when `--config` is omitted. CLI execution
denies approval-dependent tools unless explicitly given `--approve-tools`.
`reset` and `cleanup` require `--discard`; inspect affected worktrees first.
The existing hierarchical mode below remains available for non-Git workspaces.

## Create and resume a worker chat

1. Open **Workers → New worker chat** and enter the task for Reasoner.
2. Choose the project workspace and configure all three profiles. Select a CLI
   and its model, or a native OpenAI-compatible endpoint and model. A CLI can be
   selected in multiple profiles with different models.
3. Set each profile's instructions, input/timeout limits and edit/command
   permissions. Native profiles also require an output token limit. External
   CLI profiles use the selected CLI's own output limits.
4. Create the chat. Follow the task board, worker assignments and activity
   console. **Open execution window** opens an independent Desktop monitor;
   **Open execution panel** opens a VS Code panel. Closing a monitor does not
   stop the run. Execution and encrypted persistence stay in the owning process.

Profiles are stored with each new chat. Saving new defaults does not change
existing chats. Saved runs can be renamed, stopped, deleted after stopping,
and continued with a follow-up to Reasoner. Closing and reopening the app
restores the saved runs. A previously running task is marked cancelled with an interruption notice; review recorded activity and workspace state
before continuing.

Follow-up reinjects bounded excerpts from the last three user/result pairs;
children still receive only their delegated tasks. The coordinator does not
restore third-party CLI session histories or roll back filesystem changes.

Each new invocation records its worker ID, parent invocation, task ID (parallel
tasks), actual CLI/runtime, model, workspace, phase and call number. The monitor
groups assignments rather than mixing concurrent tasks that use the same tier.
Handoffs name their destination; child results return to the recorded parent.
Activity filters separate input/output, tool events, handoffs, errors and
**reported reasoning**. Reasoning appears only when the CLI/provider exposes it;
no private model reasoning is inferred. A live call shows elapsed time even if
the backend sends no progress. Historical snapshots without these fields remain
readable, using their saved task/profile routes.

**Run settings** edits profiles on an inactive saved run: CLI, model, instructions,
permissions and limits. Changes affect future calls and survive app restarts;
recorded invocation routes and explicit task overrides retain their values.
The workspace cannot be changed. A failed planning call without tasks can be
retried explicitly with **Retry planning** after reviewing partial activity and
adjusting its settings. Existing tasks use their review/reset controls instead.
Timeout failures report the worker, CLI/model and phase. No failed mutation is
automatically retried. Pending approvals remain available in the main window
and are recovered when opening the independent monitor.

Refreshes run sequentially, slow down while idle and pause requests while the
window is hidden; active runs continue in the core. Activity previews are bounded
by the retained event history, rather than loading third-party CLI session logs.

For local server routing, detected context windows and output reservations,
see [PrAImate CLI](native-cli.md#context-and-output-budgets).

## Communication and tools

PrAImate coordinates workers using one JSON action per response. The coordinator
passes the result of each action back on the next turn; this protocol does not
require an additional MCP server. Primary may delegate to Middle or
Fast, and Middle may delegate to Fast. A child receives its scoped task and
instructions; its result goes back to its caller as bounded evidence.

Supported external adapters include Codex, Claude Code, OpenClaude, GitHub Copilot,
OpenCode and PrAImate Code. Antigravity is available for native chats and terminals,
but not managed Workers: its plan mode does not enforce a read-only tool policy.
Selecting `praimate-cli` resolves the saved core host/model route. Third-party CLI adapters run with their existing
safe permissions. Workspace reads, edits and commands requested through the
JSON protocol execute through the PrAImate host and its configured approvals.
Native workers use the core model transport and validate their input/output
budget before sending the request. Their model transport has no direct tools;
the coordinator executes the returned host action. Edit and command permissions
are selected independently; commands and brokered writes use the host approval
flow. Bounded exact replacements require the profile's edit permission.

## Execution limits and verification

Initial tasks are limited to 16 KiB and follow-up requests to 8 KiB. Reference
workspace paths so workers can read focused ranges instead of pasting files.

Each tier has at most 16 model turns per invocation, with a shared maximum of
64 host dispatches. There is no additional fixed ten-minute deadline for the
whole run. Each model call uses its profile timeout (0–3600 seconds); 0 disables
that timeout, while explicit Stop and parent cancellation remain available.
New GUI profiles default to 600 seconds for Reasoner and 300 for other workers;
saved profiles keep their configured values. An external CLI can make several
provider calls within one dispatch. A malformed response or failed read
gets one correction opportunity; consecutive failures stop that invocation.
Child failures return to the parent with a warning about possible prior effects.
At most three worker chats can run concurrently, including continued chats.

These bounds control work but do not prove token savings. Supported CLI adapters
forward provider-reported token usage to the worker runtime and encrypted usage
dashboard. Missing reports remain unavailable. Integration
tests verify routing, context isolation, host operations and persistence using
controlled model responses. Live model quality and task-specific savings still
require testing with the selected CLI/model combination.
