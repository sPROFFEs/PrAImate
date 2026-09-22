# Studio extension review — 2026-09-21

Scope: current working tree based on `9181d9c`, extension **0.6.1**. This is a
self-review, not an independent review. The parity pass implements the gaps that
can safely share Core with an IDE extension and records the remaining
application-level boundaries explicitly.

## Resolved defects

### P2 — Multi-root context/workspace mismatch

Studio now tracks the selected workspace in the session, exposes a folder
picker, and asks before switching when the active editor belongs to another
root. A switch starts a new chat. The backend independently rejects editor
content whose absolute path is outside the chat workspace, so a modified or
older extension cannot bypass the boundary.

### P2 — Permission reset while CLI detection is pending

The permissions selector now retains the persisted value until capabilities
arrive. A regression test starts with `full`, edits another field while the CLI
catalogue is empty, and verifies that Save still submits `full`.

## Desktop parity inventory

| Area | Studio currently provides | Missing compared with Desktop |
| --- | --- | --- |
| Native CLI terminals | Launch/picker for Claude, OpenClaude, Codex, OpenCode and PrAImate Code; selected model; Windows shim hardening; install/update actions | Native terminals deliberately retain each vendor CLI's own permissions/configuration; VS Code owns their PTY lifecycle, so Desktop's in-process PTY archive is not shared |
| Chat | Streaming, stop/history/rename/delete, workspace-safe editor context, managed attachments, `!` commands, Markdown/code actions, restored tool activity | Inline image preview is represented as an attachment rather than a data-URL thumbnail |
| Agents | Select/details, guided creation, canonical YAML create/edit/save, reviewed pack import/export/delete, knowledge files | Graphify index building remains an explicit external tool operation; requirements can be authored in YAML and round-trip in packs but have no separate form |
| Workflows | Catalogue, input prompts and execution; full workflow authoring/editing in canonical agent YAML | No second visual workflow builder; YAML is the authoritative editor |
| Skills | Select/inherit/none, rollout enablement, directory/ZIP/GitHub inspect+install, exact-file review, approve/revoke/forget | Draft file authoring remains JSON/YAML-oriented rather than a dedicated multi-file visual editor |
| MCP | Session selection plus catalogue/custom add, edit, enable/disable, delete, credential input and live probe | OAuth providers expose their catalogue metadata but an embedded browser OAuth callback is not implemented |
| Runs | List/details, resume/stop, event execution and UTF-8 artifact viewer | A run not executing in this backend has no process to cancel; persisted resumable runs can be resumed then stopped |
| Local models | Raw routing plus shared endpoint profiles, encrypted-key preservation, model discovery and connection test | Model installation is provider-specific and remains a native provider/CLI operation |
| Approvals | Allow once, deny, and remember a tool for the current managed run | Native vendor terminals continue using their own prompt/permission systems |
| Application settings | Session settings, workspace, privacy patterns, CLI detection/install/update, diagnostics, hide/show Desktop with disconnect restoration | Git backup/sync and destructive reconciliation stay in Desktop because they own application lifecycle and machine identity |

The extension RPC surface delegates these operations to Core rather than
duplicating storage or execution rules. `runs.cancel` cancels the execution
owned by this connection; persisted runs are resumed through Core before they
can own a cancellable process.

All credential prompts run in the extension host, not the HTML webview. Agent
packs and skill sources require content-bound review digests before import or
approval. Installer commands are shown verbatim and require a native modal
confirmation before execution.

## Native terminal implementation and verification

- Desktop and Studio share `core.InteractiveCLICommand`, including Codex's `-m`
  versus the other CLIs' `--model` flag.
- `terminals.prepare` resolves the executable on the backend (including managed
  PrAImate Code outside PATH), verifies the workspace, and returns only the
  launch plan and PATH overlay. It neither executes commands nor rewrites
  project instructions/configuration. The authenticated extension host starts
  the process in Codium's real terminal.
- Native executable arguments remain separate values. Windows `.cmd`/`.bat`
  shims use explicit `cmd.exe` quoting with AutoRun and delayed expansion
  disabled. Paths/models containing unsafe batch metacharacters fail with an
  actionable error; they are not interpolated into a command.
- The terminal UI explicitly states that native CLI permissions/configuration
  apply. The Studio connection token is removed from the child environment.
- Sixteen JavaScript tests pass, including all five launch choices, model
  isolation, cancellation of the picker, trust checks and Windows argument
  construction. Focused Go tests cover mapping, resolution, managed paths,
  authentication, and absence of chat/session mutations. Desktop terminal
  regressions passed.
- Desktop window control is authenticated and changes visibility only; it does
  not stop Core. The shared server counts Studio connections and restores a
  hidden Desktop 1.5 seconds after the last connection closes, while cancelling
  restoration if another Studio window reconnects. Closing Desktop remains
  independent and leaves Codium running with its existing offline indicator.
- A real Codium test with an isolated profile loaded extension 0.6.1, connected
  to a fixture Core, completed both webview ready handshakes, and executed five
  harmless CLI fixtures in actual terminals. It checked their working folders,
  model arguments and removal of the Studio token. This is not an authenticated
  live-provider test of Claude/Codex/etc.

Current build verification: `go test ./internal/core ./internal/studio`, the
real-Codium smoke, Linux
CLI/backend and Desktop builds, and the Windows amd64 Desktop cross-build pass
with Go 1.26.1. Remaining validation is native Windows execution,
authenticated vendor sessions, and provider-specific
OAuth/model-install flows.
