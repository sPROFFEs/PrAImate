# Workers

Workers coordinates three independently configured model profiles: **Reasoner**
(`primary`), **Middle** and **Fast**. The feature is available in the Desktop
Workers page and the Studio / VS Code extension's **PrAImate: Workers** view.

## Create and resume a worker chat

1. Open **Workers → New worker chat** and enter the task for Reasoner.
2. Choose the project workspace and configure all three profiles. Select a CLI
   and its model, or a native OpenAI-compatible endpoint and model. A CLI can be
   selected in multiple profiles with different models.
3. Set each profile's instructions, input/timeout limits and edit/command
   permissions. Native profiles also require an output token limit. External
   CLI profiles use the selected CLI's own output limits.
4. Create the chat. Follow requests, responses, host tools and delegation in
   three horizontal panes; collapse panes to focus on a worker.

Profiles are stored with each new chat. Saving new defaults does not change
existing chats. Saved runs can be renamed, stopped, deleted after stopping,
and continued with a follow-up to Reasoner. Closing and reopening the app
restores the saved runs. A previously running task is marked cancelled with an interruption notice; review recorded activity and workspace state
before continuing.

Follow-up reinjects bounded excerpts from the last three user/result pairs;
children still receive only their delegated tasks. The coordinator does not
restore third-party CLI session histories or roll back filesystem changes.

The Desktop view shows the latest 80 activity entries per tier. **Show earlier
activity** reveals more retained entries without expanding all three histories
at once. Refreshes run sequentially, slow down while idle and pause requests
while the window is hidden; active runs continue in the core.

For local server routing, detected context windows and output reservations,
see [PrAImate CLI](native-cli.md#context-and-output-budgets).

## Communication and tools

PrAImate coordinates workers using one JSON action per response. The coordinator
passes the result of each action back on the next turn; this protocol does not
require an additional MCP server. Primary may delegate to Middle or
Fast, and Middle may delegate to Fast. A child receives its scoped task and
instructions; its result goes back to its caller as bounded evidence.

Supported external adapters include Codex, Claude Code, OpenClaude, OpenCode and
PrAImate Code. Selecting `praimate-cli` resolves the saved core host/model route. Third-party CLI adapters run with their existing
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
64 host dispatches and 10 minutes for the run. An external CLI can make several
provider calls within one dispatch. A malformed response or failed read
gets one correction opportunity; consecutive failures stop that invocation.
Child failures return to the parent with a warning about possible prior effects.
At most three worker chats can run concurrently, including continued chats.

These bounds control work but do not prove token savings. Third-party CLIs do
not currently provide normalized token usage to the worker runtime. Integration
tests verify routing, context isolation, host operations and persistence using
controlled model responses. Live model quality and task-specific savings still
require testing with the selected CLI/model combination.
