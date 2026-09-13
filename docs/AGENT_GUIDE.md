# PrAImate Agent Creation Manual

This manual explains how to create, configure, test, import, and export a
PrAImate agent. It also documents every field supported by the
`praimate.agent/v1` YAML format.

An agent combines:

- a system prompt (`instructions`);
- the CLI backends and GUI surfaces from which it may run;
- optional workflows;
- optional references to locally configured MCP servers;
- optional raw or RAG knowledge;
- an optional, manually run requirements script.

Agent definitions do not contain model credentials, Local LLM settings, CLI
login state, skills, chats, sessions, or project files.

## Create your first agent

1. Open **Agents** and select **+ New agent**.
2. Enter a valid YAML definition in the editor. Start with this minimal agent:

   ```yaml
   schema: praimate.agent/v1
   id: project-reviewer
   name: Project Reviewer
   description: Reviews a project and reports practical improvements.
   instructions: |
     You are a careful software reviewer.
     Inspect the project before reaching conclusions.
     Report findings in severity order and cite relevant file paths.
   supports:
     - claude
     - codex
     - opencode
     - praimate-code
   surfaces:
     - chat
     - terminal
     - editor
   ```

3. Save the agent. It now appears in the Agents list and can be launched on
   its allowed surfaces with any installed CLI listed in `supports`.

The YAML editor validates the complete definition on save. Correct the first
reported validation error and save again.

### What you built

You now have a reusable agent whose behavior is defined by a portable YAML
file. It can run through the declared CLI backends on all three GUI surfaces.
The remaining sections add repeatable workflows, knowledge, MCP integrations,
and packaging.

## Build a complete agent

The following example exercises the main portable fields:

```yaml
schema: praimate.agent/v1
id: project-reviewer
name: Project Reviewer
description: Reviews a repository against a user-selected concern.
icon: search
instructions: |
  You are a senior software reviewer.
  Read the relevant project files before answering.
  Separate verified findings from suggestions.
  Cite file paths and explain the practical impact of every finding.
supports:
  - claude
  - openclaude
  - codex
  - opencode
  - praimate-code
tools:
  - file-read
  - file-search
  - git
mcp_servers:
  - local-filesystem
surfaces:
  - chat
  - terminal
  - editor
knowledge: raw
workflows:
  - name: Review project
    description: Inspect a project for a particular class of problem.
    inputs:
      - name: focus
        prompt: What should the review focus on?
        type: string
        required: true
        placeholder: Security, performance, maintainability...
      - name: depth
        prompt: How detailed should the review be?
        type: string
        default: concise
    steps:
      - kind: user_message
        template: |
          Review this project for {{ .focus }}.
          Produce a {{ .depth }} report.
      - kind: wait_for_assistant
      - kind: user_message
        template: |
          Re-check the three highest-impact findings. Remove any finding
          that is not supported by the project files, then give the final
          report.
default_workflow: Review project
```

Before using this exact example, either create and enable an MCP server whose
ID is `local-filesystem`, or remove the `mcp_servers` block. MCP IDs are local
references, not embedded server definitions.

## Choose good instructions

`instructions` is the agent's system prompt. It should define stable behavior,
not the one-off request that belongs in a chat or workflow input.

A useful instruction block normally covers:

- role and domain;
- expected evidence and output format;
- constraints and prohibited assumptions;
- when to ask for clarification;
- how to use the attached knowledge or project files.

Keep secrets out of agent YAML. YAML and agent packs are designed to be
shared.

## Select supported CLIs and surfaces

`supports` must contain at least one of:

| Value | Backend |
|---|---|
| `claude` | Claude Code |
| `openclaude` | OpenClaude |
| `codex` | Codex CLI |
| `opencode` | OpenCode |
| `praimate-code` | PrAImate Code |

The selected CLI must also be installed and authenticated on the computer
running the agent. A declaration in `supports` does not install a CLI.

`surfaces` may contain:

| Value | GUI launch surface |
|---|---|
| `chat` | Interpreter-style Chats page |
| `terminal` | Live CLI terminal |
| `editor` | Document/editor session |

Omit `surfaces`, or use an empty list, to allow the agent everywhere. Surface
names do not include `code` or `workflow`; those are not valid
`praimate.agent/v1` values.

## Add knowledge

Save the agent before attaching knowledge. The Agents editor then exposes the
**Knowledge base** panel, where files and folders can be added.

### Raw documents

Set `knowledge: raw` or select **Raw documents**. PrAImate exposes `knowledge.read`
and `knowledge.search` for inspecting and searching the managed knowledge files.

Raw mode is the simplest choice for a small, focused collection. It requires
no indexing backend or API key.

### RAG with Graphify

Set `knowledge: rag` or select **RAG (graphify)**, install the bundled
Graphify tool when prompted, then select **Build RAG index**.

In RAG mode, PrAImate exposes a complete three-tier retrieval model:
- `knowledge.query`: Graphify-backed conceptual and relational retrieval (default budget: 1200 tokens).
- `knowledge.search`: Bounded literal text search across knowledge files (excluding `graphify-out/`).
- `knowledge.read`: Exact file reading for source verification.

The intended workflow is: Graphify identifies relevant areas and relationships, `knowledge.search` locates exact identifiers or configuration terms, and `knowledge.read` verifies the original source before taking decisions.

Available indexing backends are:

| Backend | Use |
|---|---|
| Claude CLI | Uses an installed, signed-in Claude CLI; no API key field |
| Code only | Builds code structure without semantically indexing documents or PDFs |
| Local LLM — OpenAI compatible | Uses the endpoint and protected API key configured on Local LLM |
| Ollama — optimized local backend | Uses the configured local endpoint with Ollama-specific handling |
| Anthropic API | Uses an Anthropic API key entered for the build |
| OpenAI | Uses an OpenAI API key entered for the build |
| Kimi (Moonshot) | Uses a Kimi API key entered for the build |

Local backends require the exact served model name. Remote HTTP endpoints send
knowledge over plaintext; use HTTPS unless the endpoint is trusted and local.
Cloud backends send indexed document content to the selected provider and may
incur token charges.

The RAG index is stored under the agent's knowledge folder and is included in
a `.praimate-agent` pack. Rebuild the index after changing source files. After
moving a pack between systems, rebuild if Graphify reports an incompatible or
stale index.

## Add MCP servers

Configure and enable the server first on the **MCP** page. Add its PrAImate ID
to `mcp_servers`:

```yaml
mcp_servers:
  - local-filesystem
  - project-database
```

At launch, PrAImate resolves these IDs and writes project-scoped MCP
configuration for Claude/OpenClaude, Codex, OpenCode, or PrAImate Code.
Disabled referenced servers are skipped. A missing referenced ID causes the
launch to fail with an MCP resolution error.

An agent pack does **not** include MCP definitions, commands, URLs,
credentials, or OAuth state. The recipient must create matching MCP records
with the same IDs. A requirements script may install an MCP executable, but
it does not create the PrAImate MCP record.

## Add workflows

A workflow is a named, linear sequence of prompts. It is useful for repeatable
jobs such as reviews, migrations, or report generation.

Each workflow may define:

- `name`: required and unique within the agent;
- `description`: optional user-facing explanation;
- `inputs`: optional values collected before the run;
- `steps`: one or more ordered steps.

If an agent has one workflow, that workflow is selected automatically. If it
has several, set `default_workflow` to an exact, case-sensitive workflow name
or let the user select one.

### Workflow inputs

```yaml
inputs:
  - name: target
    prompt: What should be reviewed?
    type: string
    required: true
    placeholder: src/auth
    default: src
```

| Field | Required | Meaning |
|---|---:|---|
| `name` | Yes | Unique template key |
| `prompt` | No validation requirement | Label shown to the user; provide one for usable forms |
| `type` | No | `string`, `text`, `int`, or `bool`; currently advisory |
| `required` | No | Rejects a blank value unless a nonblank default exists |
| `placeholder` | No | Input hint |
| `default` | No | Used when the supplied value is absent or blank |

The current runner supplies inputs as strings. The type field documents intent
but does not perform integer or boolean conversion.

### Template variables

`user_message.template` uses Go `text/template` syntax:

| Expression | Value |
|---|---|
| `{{ .agent }}` | Agent display name |
| `{{ .agent_id }}` | Agent ID |
| `{{ .workflow }}` | Workflow name |
| `{{ .inputs.target }}` | Input named `target` |
| `{{ .target }}` | Shortcut for the same input |

Use the `.inputs.<name>` form if an input is named `agent`, `agent_id`,
`workflow`, or `inputs`. Referencing an undeclared or missing template key is
an error. Supplied values not declared under `inputs` are ignored.

### Workflow steps

Two step kinds are accepted:

```yaml
steps:
  - kind: user_message
    template: Review {{ .target }}.
  - kind: wait_for_assistant
    until_tool: complete
```

- `user_message` renders and sends its nonblank `template`.
- `wait_for_assistant` is an explicit barrier after the preceding assistant
  reply. `until_tool` is retained in the format for compatibility, but the
  current runner does not wait for a particular tool call.

The first message runs as a new CLI turn. Later messages resume the native CLI
session where supported. Running all workflows together requires a resumable
CLI because context is shared between workflows.

Workflow execution currently uses the runner's full tool mode. The agent-level
`tools` list does not restrict it.

## Attach a requirements script

Use Agent Studio's requirements section after saving the agent:

1. Select `linux` or `windows`.
2. Add instructions explaining what the script changes and any prerequisites.
3. Select **Attach requirements script…** and choose the script.

The resulting YAML metadata looks like:

```yaml
requirements:
  os: linux
  script: setup.sh
  instructions: Installs the local parser used by this agent.
```

Rules:

- `os` must be `linux` or `windows`;
- `script` must be a filename, not a path;
- the selected file must be no larger than 2 MiB;
- scripts are included only in a `.praimate-agent` pack;
- scripts never run during import;
- the recipient must explicitly select **Run requirements script**;
- PrAImate refuses to run a script for the other operating system.

Linux scripts run through Bash. On Windows, `.cmd` and `.bat` use `cmd.exe`;
other scripts use PowerShell. Requirements scripts can install software and
modify the machine, so inspect imported scripts before authorizing them.

## Use Agent Studio

Agent Studio begins with Guided, Manual, and Import choices. Guided creation
collects a name and purpose, knowledge mode, explicit capabilities, and one of
three presets. Its final screen previews the effective capability summary and
warnings before writing anything. Manual creation opens the familiar YAML
editor. Import accepts either YAML or a complete pack.

The generated files remain transparent: `agent.yaml` is the portable persona
and workflow definition; optional `runtime.json` declares advanced runtime
intent. Attached knowledge and an optional requirements script remain separate
managed files. The helper can draft changes through an installed CLI, but
helper output is not automatically trusted or silently applied: review the
resulting files, reload them into the editor if needed, then save.

For direct, precise changes, the Agents page YAML editor is the canonical
definition editor.

## Test and run an agent

Before exporting:

1. Save the YAML with no validation errors.
2. Confirm every CLI in `supports` that you plan to use is installed and
   authenticated.
3. Confirm every referenced MCP ID exists and is enabled.
4. Choose a real project folder.
5. Launch the agent on each allowed surface you intend to support.
6. Run every workflow with blank, default, and representative input values.
7. For RAG agents, query facts that exist only in the knowledge files.
8. If present, inspect and test the requirements script on its target OS.

CLI-only automation can list and run installed agents:

```bash
praimate -list-agents
praimate -run-agent project-reviewer
```

### Headless agent API

Use the versioned agent-run interface when another program needs PrAImate as
an intermediary to an installed CLI/model:

```bash
praimate agent run \
  --agent project-reviewer \
  --cli praimate-code \
  --folder /srv/source \
  --prompt "Review this code"
```

The compact spelling below is equivalent:

```bash
praimate --agent project-reviewer --cli praimate-code --folder /srv/source --prompt "Review this code"
```

To execute a named workflow through the same machine API, use `--workflow` and
repeat `--input key=value` for its declared inputs. `--run-id` enables encrypted
status lookup and idempotent completed-result replay. The complete protocol,
model preflight command, retry semantics, and Python capture example are in the
[CLI agent API guide](CLI_AGENT_API.md).

The default `--output json` writes exactly one
`praimate.agent-run/v1` object to stdout. A successful result contains
`ok`, `agentId`, `agentName`, `cli`, `runtime`, `reply`, and `durationMs`.
Failures use the same schema with `ok: false`, an `error`, and a non-zero exit
status. `--output jsonl` emits live `type: "event"` records followed by one
`type: "result"` record. `--output text` is intended for humans, not parsers.

Useful options:

| Option | Behavior |
| --- | --- |
| `--cli NAME` | Select the CLI adapter that executes the agent. |
| `--model NAME` | Pin a cloud model, or the model served by `--endpoint`. |
| `--endpoint URL` | Use a local/OpenAI-compatible route; requires `--model` and loads its API key from encrypted settings. |
| `--prompt-file PATH` | Read the prompt from a file; `-` means stdin. With no prompt flag, piped stdin is used automatically. |
| `--workflow NAME` | Execute an exact named agent workflow instead of a prompt. |
| `--input KEY=VALUE` | Supply one workflow input; repeat for multiple values. |
| `--timeout 30m` | Cancel the run at the deadline; `0` disables it. Timeout exits with status 124. |
| `--tools safe` | Default. Explicit read/answer-only policy, even when the agent manifest has a wider default. |
| `--tools edits` | Permit declared project-file writes when the selected runtime/CLI can enforce that level; otherwise it degrades to Safe. |
| `--tools full` | Approve every capability declared by the agent. Use only for a trusted agent and folder. |
| `--persist` | Keep the backing chat and return `chatId`; otherwise the temporary chat/messages are deleted after the result. |
| `--run-id ID` | Enable encrypted durable status and exact-request result replay. |

For secure-storage unlock, the command first uses a password remembered in the
OS credential store. If it is not remembered and stdin is a terminal, PrAImate
asks for it with input echo disabled. Non-interactive jobs fail immediately
with structured output unless they supply the secret through an approved
channel. A database password is never accepted in argv because process
listings may expose command-line values.

See the dedicated [CLI agent API guide](CLI_AGENT_API.md) for the complete
contract, unlock precedence, exit codes, and a link to the runnable Python
example.

Agent creation, YAML import, pack import, and export are GUI operations in the
current release.

## Export and import

The Agents page accepts `.yaml`, `.yml`, `.praimate-agent`, and `.zip`
imports.

### Bare YAML

Bare YAML carries only the serialized fields in this manual. It is appropriate
for prompt-and-workflow agents that have no attached files.

It does not carry knowledge files, a RAG index, or the requirements script
file. The `mcp_servers` values remain references to the recipient's local MCP
records.

### Agent pack

The normal portable format is `.praimate-agent`, which is a ZIP archive:

```text
agent.yaml
runtime.json
knowledge/**
requirements/**
```

The complete managed knowledge folder is included, including a built Graphify
index. The complete requirements folder is also included. `runtime.json` is
included only when advanced capabilities have been configured.

Import validates and extracts a pack in a staging directory before replacing
live agent data. Importing an existing agent ID updates its definition and, for
a pack, replaces its runtime manifest, managed knowledge, and requirements
folders. A pack without `runtime.json` removes an older manifest for that agent
instead of inheriting stale capabilities. Review an imported definition and
any script before running it.

### What a pack does not contain

- MCP server definitions or MCP secrets;
- Local LLM endpoints, API keys, or model settings;
- cloud-provider API keys used to build RAG;
- CLI executables, authentication, or account state;
- Graphify or other managed tool executables;
- skills;
- chats, sessions, schedules, watchers, or projects.

## Complete YAML reference

### Agent fields

| Field | Type | Required | Rules and behavior |
|---|---|---:|---|
| `schema` | string | Yes | Must be exactly `praimate.agent/v1` |
| `id` | string | Yes | Stable storage/import identity; use lowercase letters, digits, and hyphens |
| `name` | string | Yes | User-facing display name |
| `description` | string | No | Short summary shown in the GUI |
| `icon` | string | No | User-facing icon identifier; arbitrary string |
| `instructions` | string | Yes | System prompt sent to the underlying CLI |
| `supports` | string list | Yes | One or more supported CLI identifiers |
| `tools` | string list | No | Descriptive capability metadata only in the current release |
| `mcp_servers` | string list | No | IDs of locally configured MCP records |
| `workflows` | workflow list | No | Named input-and-step sequences |
| `default_workflow` | string | No | Exact name of a declared workflow |
| `surfaces` | string list | No | Any of `chat`, `terminal`, `editor`; omitted means all |
| `knowledge` | string | No | `raw`, `rag`, or omitted |
| `requirements` | object | No | One target OS, script filename, and optional instructions |

Unknown YAML keys are ignored by the current decoder. Do not rely on this for
forward compatibility: misspelled optional fields may therefore appear to
save while having no effect.

### Important behavior of `tools`

`tools` is stored and exported, but it does not currently install tools,
translate names into CLI permissions, or enforce an allowlist. Runtime tool
availability comes from the selected CLI, its configuration, the launch
surface, and any MCP configuration. Treat `tools` as documentation for humans
and future integrations.

## Runtime manifest reference

`runtime.json` is optional and stored beside the agent's managed folders. If
it is absent, the effective mode is `native` and behavior is identical to an
existing agent. The decoder rejects unknown fields and multiple JSON values so
typos and appended content cannot silently change the security boundary.

```json
{
  "schema": "praimate.runtime/v1",
  "preset_origin": "tool-enabled",
  "mode": "native",
  "capabilities": {
    "read_project": true,
    "analyze_code": true,
    "modify_files": true
  },
  "features": {},
  "permissions": {
    "default_tools": "edits"
  }
}
```

| Field | Values and behavior |
|---|---|
| `schema` | Required; exactly `praimate.runtime/v1` |
| `preset_origin` | Optional provenance: `simple`, `tool-enabled`, `autonomous`, `team`, or `custom` |
| `mode` | Required: `native` or `agentic` |
| `capabilities` | Boolean intent flags: `read_project`, `analyze_code`, `use_git`, `execute_commands`, `modify_files`, `network`, `external_services` |
| `features` | Managed-runtime requirements: `managed_tools`, `working_memory`, `sandbox`, `artifacts`, `checkpoints`, `delegation`, and `max_children` |
| `permissions.default_tools` | Optional native CLI preference: `ask`, `edits`, `plan`, or `full`; unsupported levels degrade to the CLI's safe mode with a warning |
| `limits` | Optional managed limits: `max_turns` per execution/resume attempt (1–100), `max_context_chars` (at least 2000), and `max_output_chars` (at least 500) |

Native manifests cannot claim managed features. Agentic manifests must request
at least one managed feature. The current executable single-agent profile
requires all three implemented modules (`managed_tools`, `working_memory`, and
`artifacts`); a partial profile is stored but rejected before launch rather
than silently changing its semantics. Delegation requires `max_children` from
1 to 32; the Team preset currently expands to 4.

The current managed runtime supports `managed_tools`, `working_memory`,
`artifacts`, and `checkpoints` for a single agent on Chat, document Studio, and
Workflow surfaces. It requires explicit JSON lifecycle actions and stores only
functional run state, per-run memory, checkpoints, and artifacts—no event log.
The always-available broker tools are `memory.task`, `memory.note`,
`memory.fact`, `memory.decision`, and `artifact.write`; large output is bounded
and preserved as an artifact.

Runtime capabilities expose additional tools:

- `read_project`, `analyze_code`, or `modify_files`: `project.list`,
  `project.read`, and literal `project.search`, contained beneath the selected
  working folder;
- `modify_files`: atomic `project.write`, after approval;
- `use_git`: `git.run`; read-only inspection is automatic and mutations need
  approval;
- `execute_commands`: argv-only `command.run`, after approval, with a fixed
  working folder, reduced environment, timeout, and bounded output;
- `network`: bounded `network.get`, after approval;
- `external_services` plus `mcp_servers`: discovered MCP tools through local
  stdio, Streamable HTTP, or legacy SSE, with approval before connection and
  for every call;
- `knowledge: raw`: contained `knowledge.read`; `knowledge: rag`:
  `knowledge.query` against the existing Graphify index.

Stopped, failed, stalled, and orphaned `running` runs can resume from their
durable checkpoint in Agent Studio. A tool interrupted without a result is
marked outcome-unknown; recovery directs the model to inspect current state
instead of blindly replaying the operation. Credentials are never written to
`request.json`.

OS sandboxing and delegation are unavailable. A manifest that requests either
is rejected before the CLI starts. Team therefore remains blocked and is not a
guided-creation choice. Interactive Terminal launches remain native. The
selected CLI adapter must explicitly guarantee an enforceable headless safe
mode. An adapter that ignores safe permissions is rejected before its process
starts. Approved commands are real host processes; the Autonomous preset does
not imply OS-level isolation.

### Validation rules

An agent is rejected when:

- `schema` is missing or not `praimate.agent/v1`;
- `id`, `name`, or `instructions` is blank;
- `supports` is empty or contains an unknown CLI;
- workflow names or input names are duplicated;
- a workflow has no steps;
- a workflow input type or step kind is unknown;
- a `user_message` has a blank template;
- `default_workflow` does not match a workflow;
- a surface or knowledge mode is unknown;
- requirements use an unsupported OS or a path instead of a script filename.

Workflow names and IDs referenced elsewhere are case-sensitive.

## Portability and security

- Agent YAML and packs are shareable artifacts. Never place passwords, API
  keys, bearer tokens, or private endpoints in them.
- MCP and Local LLM secrets remain in PrAImate's protected local storage and
  are resolved only on the receiving system.
- Raw knowledge is exposed to the selected CLI so it can read the files.
- RAG indexing may transmit knowledge to the selected indexing provider.
- A pack contains the knowledge source files themselves, not only the index.
- Requirements scripts are executable code. Import does not run them, but
  manually running one grants it the current user's permissions.
- Instructions and knowledge are untrusted input to the underlying model.
  Review imported agents before using them on sensitive projects.

## Troubleshooting

| Problem | Likely cause and action |
|---|---|
| `unknown CLI ... in supports` | Use one of the five exact CLI values in this manual |
| `unknown surface` | Use `chat`, `terminal`, or `editor` |
| `default_workflow ... does not match` | Match the workflow name exactly, including case |
| Workflow input is required | Supply a nonblank value or define a nonblank `default` |
| Workflow template execution fails | Declare every referenced input and check template spelling |
| Agent launch reports an MCP error | Create the referenced MCP ID locally and enable it, or remove the reference |
| RAG cannot build | Install Graphify, add files, choose a working backend, and supply the required model/key |
| Local RAG reports connection errors | Verify the Local LLM endpoint, exact model name, API key, and OpenAI-compatible API path |
| Requirements script is unavailable after YAML import | Export/import a `.praimate-agent` pack instead of bare YAML |
| Imported RAG answers are stale or fail | Rebuild the Graphify index on the receiving system |
| An agent does not appear on a launch surface | Add that exact value to `surfaces`, or omit `surfaces` |
| A value in `tools` has no effect | Expected today: `tools` is metadata, not runtime policy |

## Pre-share checklist

- YAML saves successfully under `praimate.agent/v1`.
- Instructions contain no secrets or machine-specific assumptions.
- Supported CLIs and surfaces are accurate.
- Every workflow runs with representative inputs.
- MCP references and external prerequisites are documented.
- Raw knowledge contains only files intended for the recipient.
- RAG was rebuilt after the last knowledge change.
- Requirements scripts were inspected and tested on the declared OS.
- `.praimate-agent` was used when knowledge or a script must travel.
- The exported artifact was imported into a clean test profile before
  distribution.
