# CLI integrations and usage metrics

PrAImate can launch installed CLIs in Desktop, Studio and the VS Code extension.
The Desktop **CLIs** page provides installation and update commands. Authentication
still belongs to each CLI: complete its sign-in in an interactive terminal before
using it as a chat backend. Installing a CLI does not grant access to its models.

## Backends

| Backend ID | Executable | Streaming chat and resume | Managed Workers | Tokens from terminals launched by PrAImate |
| --- | --- | --- | --- | --- |
| `praimate-cli` | `praimate-cli` | Yes | Yes | Through its core chat |
| `claude` | `claude` | Yes | Yes | OpenTelemetry API request reports |
| `openclaude` | `openclaude` | Yes | Yes | Compatible Claude telemetry reports |
| `codex` | `codex` | Yes | Yes | OpenTelemetry completed response reports |
| `opencode` | `opencode` | Yes | Yes | Per-launch usage plugin |
| `praimate-code` | `praimate-code` | Yes | Yes | Per-launch usage plugin |
| `copilot` | `copilot` | Yes | Yes | OpenTelemetry model call reports |
| `antigravity` | `agy` | Yes | No | Not available |

Token coverage depends on the installed CLI version and the provider returning
usage. Unsupported or missing reports remain **unavailable**, never estimated
from message length. External terminals opened independently of PrAImate are not
tracked. This feature does not import past conversations or scan CLI histories.

Model fields accept the CLI's native model identifier. A blank field keeps its
default. Suggestions are examples, not an entitlement check or a complete list.
Each Worker tier can select its own CLI and model. Saved local endpoint routing
continues to use the backends that explicitly support it; adding these two CLIs
does not add arbitrary local endpoint routing to them.

### OpenCode compatibility

OpenCode **1.18.34** (the stable release checked on October 1, 2026) is compatible
with the updated adapter. The official Linux binary was tested against an
isolated OpenAI-compatible HTTP fixture: a new conversation, native session
resumption, loading a project skill, a stdio MCP call, a shell command that writes
a temporary file, JSON usage events, and the per-launch usage plugin all passed. This exercises the
adapter shared by chats and OpenCode Worker tiers; it does not test paid accounts
or an entire multi-provider Worker run.

The review found and fixed a launch bug: the process changed directory while
inheriting the Desktop/Studio parent's `PWD`. OpenCode uses `PWD` to select the
workspace, which could load a different project's configuration and cause an
internal server error. The adapter now derives its environment after setting
the working directory. The integration test deliberately starts with a different
parent `PWD` and verifies both the first turn and resumption. This correction also
applies to the shared PrAImate Code adapter; older published app binaries do not
include it until a new application build is installed.

`run --format json --thinking`, `--session`, and `--model provider/model` retain
their contracts. Full mode's `--dangerously-skip-permissions` remains a supported
alias for `--auto`; no runtime flag migration is required. Existing V1
`opencode.json` provider and MCP configuration remains supported. PrAImate Code
uses its separately bundled version and is not upgraded by this check.

The installed-binary test is optional and uses temporary configuration, session,
cache and skill directories, with model requests directed at localhost. Download
and verify the official CLI separately, then run:

```sh
PRAIMATE_TEST_OPENCODE_BINARY=/absolute/path/to/opencode go test ./internal/core -run '^TestOpenCodeInstalledCompatibility$' -count=1 -v
```

The command fixture currently requires Linux or macOS. Without the environment
variable, ordinary test runs skip this integration test. The JSON-RPC helper
accepts zero and string request IDs, including OpenCode's initialization request.

Reference: [OpenCode 1.18.34 release](https://github.com/anomalyco/opencode/releases/tag/v1.18.34)
and its [run command implementation](https://github.com/anomalyco/opencode/blob/v1.18.34/packages/opencode/src/cli/cmd/run.ts).

### GitHub Copilot CLI

- Install the official `@github/copilot` npm package from **CLIs**; Node/npm are prerequisites.
- Use `/model` in Copilot to check model identifiers available to your account.
- PrAImate sends prompts over stdin, parses JSONL output, and resumes using the
  returned session ID. A final `result` reports completion; token counts are
  collected separately from per-call OpenTelemetry reports because the current
  CLI filters SDK usage events out of JSONL.
- Safe mode exposes file reading/search tools. Edits adds file creation/editing.
  Full mode permits native commands and MCP tools. The same restrictions are
  applied again when resuming a conversation.
- Selected MCP servers use a private temporary configuration with environment
  references for credentials. PrAImate removes it after the process exits.
  The strict Safe/Edits tool allowlists exclude MCP tools; use Full when the
  conversation requires them.

See the official [programmatic reference](https://docs.github.com/en/copilot/reference/copilot-cli-reference/cli-programmatic-reference)
and `copilot help monitoring` for the installed version's telemetry options.

### Antigravity CLI

- The backend ID is `antigravity`; the official executable is `agy`.
- **CLIs** offers Google's installer for Linux/macOS/WSL or Windows PowerShell.
- Use `agy models` for supported model identifiers. PrAImate supports streaming
  input/output and saved conversation IDs for headless chat resumption.
- Native, Plan, Edits and Full map to Antigravity's own permission modes.
  Plan is an instruction-based mode and does not enforce PrAImate's read-only
  restrictions. Consequently Antigravity is unavailable for managed Workers.
- Automatic injection of PrAImate MCP servers is not supported for this backend.
  Antigravity's own MCP configuration still applies. Agent instructions supplied
  in the prompt do not imply access to the internal Skills MCP.
- Headless token metrics count completed step reports. The cumulative session
  total is ignored so resuming does not count earlier turns again. Interactive
  terminal usage has no verified exporter and is shown as unavailable.
- Installation success does not guarantee CPU compatibility. The official Linux
  binary tested during development required `pclmul`; a VM without that CPU feature
  could not run it. Check `agy --version` on the target machine.

See Google's [headless protocol](https://www.antigravity.google/docs/cli/headless/),
[installation guide](https://www.antigravity.google/docs/cli/install/) and
[mode reference](https://www.antigravity.google/docs/cli/modes/).

## Storage and counting

The dashboard stores usage rows in the current user's existing **encrypted
PrAImate database**. Rows contain CLI, model, timestamp, surface, outcome and token
counts; they do not contain prompts, responses, tool output or credentials.
CLI-owned histories and logs remain subject to the CLI's own storage policy.

Terminal launchers create an authenticated receiver bound to `127.0.0.1` with a
random port and per-launch token. Prompt/content capture is disabled in the
telemetry configuration. Only recognized token fields are retained. Duplicate
reports are ignored. Receivers and temporary plugins are removed when the
terminal closes or its owning backend shuts down. VS Code also reports failed
terminal creation so the prepared receiver can be released.

Input totals include cached input when the CLI reports it separately. Output
totals include reasoning when reported separately. Categories already included
in a provider total are not added a second time. These are usage counters, not
an invoice or a currency estimate.

Chat and Worker records represent turns/runs. Terminal records represent
individual reported model calls and are dated when received, in UTC. Dashboard
run counts therefore combine these units. Missing terminal reports (for example,
an older CLI, a hard process kill, or telemetry disabled by organization policy)
cannot be reconstructed or treated as zero usage.
