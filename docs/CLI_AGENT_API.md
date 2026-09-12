# CLI agent API

PrAImate can run an installed agent without opening the desktop application.
This makes it an adapter between scripts and the configured CLI/model while
retaining the agent's instructions, knowledge, MCP references, runtime policy,
and encrypted settings.

## Basic call

```bash
praimate agent run \
  --agent DEV-TEAM \
  --cli praimate-code \
  --folder /path/to/project \
  --prompt "Review this code and report confirmed defects"
```

`praimate --agent ...` is an equivalent compact form. The agent must already
exist in PrAImate. `--folder` must name an accessible directory. `--cli`
selects the actual adapter that executes the agent and must be one of the
agent's supported CLIs. If omitted, PrAImate uses the first supported CLI;
automation should normally specify it to avoid depending on agent ordering.

For long or sensitive prompts, prefer a protected file so the prompt does not
appear in the process list:

```bash
praimate agent run \
  --agent DEV-TEAM \
  --cli praimate-code \
  --folder /path/to/project \
  --prompt-file /path/to/protected-prompt.txt
```

To reproduce the GUI's local-endpoint selection, pass the endpoint and the
model it serves:

```bash
praimate agent run \
  --agent DEV-TEAM \
  --cli praimate-code \
  --endpoint saved \
  --model qwen3-coder \
  --folder /path/to/project \
  --prompt-file /path/to/protected-prompt.txt
```

`saved` selects the endpoint configured in the GUI's Local LLM tab. An explicit
URL is accepted only when it matches that saved endpoint. This prevents a
headless caller from redirecting the encrypted API key to another server. The
key is loaded from encrypted settings and is deliberately not accepted in
argv. Local routing is available for OpenClaude, OpenCode, and PrAImate Code.
Claude Code stays on Anthropic routing, and Codex local routing remains
unsupported. A Claude-style local run must select `--cli openclaude`.

Local runs perform an authenticated model-list preflight before launching the
CLI. This catches unloaded, renamed, and unauthorized models without starting
an agent job. Use `--skip-model-preflight` only when a compatible endpoint can
generate responses but does not expose Ollama `/api/tags` or OpenAI
`/v1/models`.

Piped stdin is also accepted when neither `--prompt` nor `--prompt-file` is
present. Do not use prompt stdin when an interactive database-password prompt
may be required: a pipe is not a terminal.

## Run a named workflow

The same interface executes workflows stored on an agent:

```bash
praimate agent run \
  --agent docx-worker \
  --cli praimate-code \
  --folder /srv/documents \
  --workflow "Rearrange document" \
  --input source=/srv/documents/input.docx \
  --input output=/srv/documents/output.docx
```

`--input` is repeatable and preserves commas and spaces in values. Every item
must be `key=value`; malformed and duplicate keys fail with exit status 2.
`--workflow` cannot be combined with `--prompt` or `--prompt-file`. The legacy
`--run-agent` interface remains for old scripts, but new automation should use
`agent run`.

## Skills and MCP in headless agent runs

When an agent or workflow declares skills or MCP servers in its definition (`agent.yaml` / `skills.lock.json`):

1. **Automatic Skills MCP Bridge:** PrAImate spins up an embedded internal Skills MCP server on a dynamic loopback port for the lifetime of the run.
2. **Dynamic Tool Execution:** The CLI receives the standard tools `list_available_skills`, `load_skill`, and `read_skill_resource`. The agent inspects available procedures and calls `load_skill(name)` on demand rather than bloating the initial prompt.
3. **User MCP Servers:** Declared MCP servers (`mcp_servers`) are resolved from encrypted settings and injected into the CLI's environment and configuration automatically.

## Database unlock

PrAImate never accepts the database password as a command-line argument. Such
arguments are commonly visible in process listings and shell history.

Unlock sources are tried in this order:

1. `PRAIMATE_DB_PASSWORD`, intended only for a child process in unattended
   automation. PrAImate removes it from its own environment immediately after
   reading it.
2. The password remembered by Windows Credential Manager or the Linux desktop
   Secret Service.
3. If stdin is an interactive terminal, a `PrAImate database password:` prompt
   on stderr. Input echo is disabled, so the password is not displayed or
   stored in shell history.
4. If no source is available in a non-interactive run, PrAImate returns a
   structured error instead of hanging.

`--db-password-stdin` is available for wrappers. On a terminal it uses the
same hidden prompt. From a pipe it reads one password line, but then the prompt
must come from `--prompt` or a real `--prompt-file`; one stdin stream cannot
carry both values.

For interactive automation, inherit stdin and stderr and capture stdout only.
That preserves the hidden prompt while keeping stdout valid JSON. The supplied
[Python example](../examples/praimate_agent_capture.py) does exactly this.

For fully unattended automation, use an OS credential store where possible.
If an environment secret is necessary, inject it into the child process from
the CI secret manager rather than exporting it in an interactive shell.

## Output contract

The default `--output json` writes exactly one
`praimate.agent-run/v1` object to stdout:

```json
{
  "schema": "praimate.agent-run/v1",
  "ok": true,
  "agentId": "DEV-TEAM",
  "agentName": "Development Team",
  "cli": "praimate-code",
  "runtime": "native",
  "runId": "run-20260825T071500-a1b2c3d4e5f60708",
  "state": "completed",
  "attempt": 1,
  "reply": "No confirmed defects found.",
  "durationMs": 8421
}
```

Failures use the same schema with `ok: false`, an `error` string, and a nonzero
exit status. Diagnostics and password prompts go to stderr, never stdout.

Other formats:

- `--output jsonl` emits zero or more `type: "event"` objects followed by one
  `type: "result"` object. Events include `runId`; tool events include `id`
  when the CLI provides one, allowing `tool_start` and `tool_end` correlation.
  This is progress telemetry, not a host-side tool callback.
- `--output text` prints only the final reply and is meant for humans, not
  stable programmatic parsing.

Exit statuses are `0` for success, `1` for an execution/configuration failure,
`2` for invalid arguments or input, and `124` when `--timeout` expires.

## Options and trust policy

| Option | Meaning |
| --- | --- |
| `--cli NAME` | Select the CLI adapter that runs the agent. |
| `--model NAME` | Override the cloud model, or select the model served by `--endpoint`. |
| `--endpoint saved\|URL` | Route through the configured local endpoint; a URL must match the saved value. Requires `--model`. |
| `--prompt TEXT` | Supply a short prompt inline. |
| `--prompt-file PATH` | Read a prompt from a file; `-` means stdin. |
| `--workflow NAME` | Execute an exact named workflow instead of a prompt. |
| `--input KEY=VALUE` | Supply a workflow input; repeat for multiple inputs. |
| `--timeout 30m` | Set the execution deadline; `0` disables it. |
| `--tools safe` | Default. Do not approve mutation-capable managed tools. |
| `--tools edits` | Permit declared project writes when enforceable. |
| `--tools full` | Approve every capability declared by the agent. |
| `--persist` | Keep the backing chat and include `chatId` in the result. |
| `--run-id ID` | Opt into encrypted durable status and completed-result replay. |
| `--retry` | Explicitly retry the identical durable request. Requires `--run-id`. |
| `--skip-model-preflight` | Skip local model-list validation for a non-standard endpoint. |
| `--output FORMAT` | Select `json`, `jsonl`, or `text`. |

Use `safe` for analysis. `edits` and especially `full` grant the selected agent
more authority over the project and must be explicit trust decisions. Without
`--persist`, PrAImate deletes the temporary chat after producing the result.

## Durable IDs, status, and retries

Every result has a generated `runId` for correlation. Generated IDs are
ephemeral: PrAImate does not create an execution log by default.

Supplying your own `--run-id` opts into a durable record inside the encrypted
PrAImate database:

```bash
praimate agent run --run-id invoice-batch-2026-08-25-0042 [other options]
praimate agent status --run-id invoice-batch-2026-08-25-0042 --output json
```

Repeating the exact request with the same ID returns the stored result with
`cached: true` and does not launch the CLI again. Reusing that ID with different
arguments or prompt/workflow content is rejected. Re-execution requires the
same request plus explicit `--retry`.

`--retry` is intentionally explicit. A native CLI may have edited a file before
its host process crashed, so PrAImate cannot prove exactly-once side effects
across that boundary. Inspect the workspace before retrying an unknown or stuck
run. Managed agents expose their internal lifecycle as `managedRunId` in
addition to the external `runId`.

## Check model readiness

Query the saved endpoint with its encrypted credential:

```bash
praimate model check --endpoint saved --model qwen3-coder --output json
```

Add `--probe` to perform a tiny safe generation. PrAImate Code is the default
probe CLI; `--cli` and `--folder` can override it:

```bash
praimate model check --endpoint saved --model qwen3-coder \
  --probe --cli praimate-code --folder /srv/source --output json
```

The `praimate.model-check/v1` response distinguishes `present` (the model is in
the authenticated catalogue) from `responding` (the optional generation probe
returned a non-empty successful response).

## Python integration

The capture example validates and atomically saves JSON responses, supports
prompt files and named workflows, and leaves the terminal attached for a hidden
database-password prompt:

```bash
python3 examples/praimate_agent_capture.py \
  --agent DEV-TEAM \
  --cli praimate-code \
  --folder /path/to/project \
  --prompt-file /path/to/protected-prompt.txt \
  --save-response /path/to/result.json
```

On Windows, run the same script with `py` and pass
`--praimate praimate.exe` if the executable is not resolved automatically.
On POSIX systems, saved responses use mode `0600`. On Windows, place the output
in a directory protected by the current user's ACL because POSIX mode bits do
not provide a Windows access-control guarantee.
