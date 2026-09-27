# PrAImate CLI

`praimate-cli` is the local-model frontend to the PrAImate core, not a separate
agent implementation. Desktop chats and workflows execute the provider inside
the core process; the terminal executable calls the same core APIs and opens
the same encrypted database. External providers can continue to use their own
CLI adapters. Native execution does not silently fall back to a cloud provider.

## Start and resume

```sh
praimate-cli --endpoint http://127.0.0.1:11434/v1 --model YOUR_MODEL --tools ask
praimate-cli --chat CORE_CHAT_ID
praimate-cli --continue
praimate-cli --model YOUR_VISION_MODEL --attach "screenshots/problem.png" "Explain this error"
praimate-cli --agent dev-team --model YOUR_MODEL "Review this project"
praimate-cli --agent dev-team --model YOUR_MODEL --workflow "Fix a bug" --inputs '{"bug":"Describe the bug"}'
```

The endpoint must implement OpenAI-compatible Chat Completions streaming and
function calling for tool use. Ollama, compatible local servers and routers
such as OmniRoute can supply the endpoint. Model IDs, including provider/model
namespaces, are passed unchanged. `/models` queries that endpoint's catalogue.
Use `--context-tokens` and `--output-tokens` to match the loaded model. Defaults
are 8192 and 1024 unless the selected core Local LLM host has configured limits.
Context budgeting reserves output tokens plus a safety margin (5% of the window,
at least 256 tokens). It estimates text, Unicode, tool schemas/calls, skill
payloads and vision input separately; base64 bytes are not counted as text tokens.
Endpoint-reported prompt usage calibrates subsequent estimates upward within
the model session. These are estimates, **not exact tokenizer counts**; image
token costs in particular differ between models. `/context` shows the last
request estimate, reservations, compactions and last reported usage.

The transport requests streaming usage when supported, and retries without that
optional field only if the endpoint explicitly rejects it. A context-length HTTP
rejection allows one attempt with a smaller input budget. Completed tools are
not replayed, and partial streams are not silently retried. If the latest
request, images, instructions or tool schemas cannot fit, the run stops with an
error rather than silently discarding the current request.

The core resolves credentials for the selected saved host. Standalone callers
can use `OPENAI_BASE_URL`, `OPENAI_MODEL` (or `PRAIMATE_MODEL`) and
`OPENAI_API_KEY`. Keys are not accepted in argv or written to project files.
Router URL prefixes are preserved; cross-origin redirects are rejected.

Storage uses PrAImate's canonical data directory, including `PRAIMATE_HOME`.
An interactive terminal asks for the database password if no remembered OS
credential is available. For automation, `--db-password-stdin` reads the
password from the first newline-terminated stdin line; provide the prompt in
argv or on the remaining stdin. A new installation asks to create a database
password. Beta plaintext session files are not imported automatically.

`--chat`/`--session` takes a **core chat ID**, not the internal model session ID.
Desktop and Studio terminals receive that identity directly, preserving their
agent, skills, local route, MCP selection, permissions and transcript. An
endpoint change on a live model session requires a new chat, preventing silent
transfer of its private context to another host.

## Permissions and tools

| Mode | Files | Commands | Network and MCP |
| --- | --- | --- | --- |
| `safe` (default) | Read/search selected workspace | Disabled | Disabled |
| `ask` | Writes require approval | Approval required | Connection/action approval required |
| `edits` | Workspace writes allowed | Approval required | Disabled |
| `full` | Workspace writes allowed | Allowed | Selected services allowed |

Agent capability restrictions further narrow these permissions. Non-interactive
Ask requests are denied, never auto-approved. File tools reject absolute paths,
parent traversal and symlinks outside the workspace. Commands and MCP servers
run with the OS user's authority: approval is **not an operating-system sandbox**.

The provider exposes core file reading/listing/searching, read-only Git inspection, exact-string edits,
writes, commands, URL fetching, raw/RAG agent knowledge and selected MCP tools.
User-selected text attachments can be read by index without granting arbitrary
filesystem access. PNG, JPEG and GIF attachments are sent as multimodal image
parts to the selected endpoint, including in native Autonomous chat runs. The
endpoint/model must support vision; there is no automatic OCR, model switch or
cloud fallback. Other image formats (including WebP/SVG), PDFs and other binary
documents require conversion to a supported image or text first.
Shell syntax requires an explicit platform shell, such as `sh` or `cmd.exe`;
ordinary commands use an executable and argument array on both platforms.

MCP uses the core's registered servers, transports, authentication and timeouts.
Choose them in Desktop/Studio or use `--mcp ID1,ID2`. A workspace `.mcp.json`
never starts processes automatically. `AGENTS.md` and `.praimate/rules.md` are
read as bounded project instructions, not permission grants.

Pinned skills and dynamic `skill_load`/`skill_read` use the existing core trust,
exact version locks, resource limits and context budgets. Skill bodies are
revalidated for every model request, not retained in native protocol checkpoints.
Autonomous agents continue through the managed runtime for working memory,
plans, checkpoints, artifacts, approvals and execution budgets; their native
provider is model-only to avoid a second, competing tool loop.

## Sessions, output and builds

The terminal includes editable prompts, arrow-key history (memory only), Home/End,
Tab completion for slash commands, multiline input (end a line with `\`), and
bracketed paste. Press Enter after pasting; pasted slash commands remain prompt
text and pasted approvals are denied. Prompts are limited to 1 MiB overall and
fewer than 4096 characters per edited line; use attachments or piped stdin for
larger inputs. Terminal control characters in model/tool output are filtered.

Interactive commands: `/help`, `/status`, `/context`, `/models`, `/model ID`,
`/tools LEVEL`, `/attach PATH`, `/attachments`, `/detach N|all`, `/mcp`, `/skills`,
`/sessions`, `/compact`, `/clear`, `/exit`. `/clear` creates a new chat and retains
the previous transcript. Automatic compaction removes older complete turns and
trims large tool results before dropping complete tool exchanges, keeping bounded
excerpts and the latest user request/images. It is lossy, not a semantic summary.
Explicit `/compact` replaces old model history with excerpts and **drops old
images**; attach them again if needed. Native
checkpoints and renewable run leases live in the encrypted core database.
Interrupted tool calls are not replayed automatically.

Use repeatable `--attach PATH` flags or `/attach PATH` (one path per command;
spaces and optional quotes supported). `/attachments` lists the next message's
queue; `/detach N` uses 1-based indexes. Enter on an empty prompt sends queued
attachments. The queue is consumed when a turn is attempted, including failed
turns, but is retained by `/compact`; `/clear` clears it. Attachment flags are
not supported for `--workflow`; use workflow inputs instead.

Up to 20 files may be selected per message. Images are limited to 4 files,
8 MiB each, 16 MiB combined, 8192 pixels per side and 32 megapixels. Retained
image payloads are also bounded to 32 MiB of encoded data by automatic compaction.
These limits do not override the selected core skills/runtime input budgets.
Native chat sessions store image snapshots in the encrypted checkpoint, so a
normal resume does not reread changed source images. An explicitly resumed
Autonomous run revalidates its selected source files. Images and image metadata
are **not text-redacted**; attach only content you want sent to that endpoint.

`--format json` emits newline-delimited core events, including a core chat ID,
text/reasoning deltas, correlated tool start/end events, structured `context`
and `usage` data in `raw`, and failure status. Text mode displays activity,
context/usage and a turn summary; `--show-reasoning` also displays reasoning text.
Errors, truncated responses and exhausted tool-turn budgets produce a nonzero
exit code for single-shot/JSON runs. The interactive prompt remains available
after a failed turn. Ctrl+C cancels the active turn or clears pending input;
Ctrl+D at an empty prompt exits. A normal native turn allows up to 64
model/tool rounds; Autonomous mode uses its configured managed budgets.

Build from the repository root with `go build ./cmd/praimate-cli`. The release
scripts bundle it for Linux and Windows (amd64/arm64); installers and the
updater manage it alongside Desktop. The GUI build script also builds its
terminal sibling. Cross-compilation does not replace a Windows runtime smoke
test, and mock-provider tests do not establish compatibility with every model.
