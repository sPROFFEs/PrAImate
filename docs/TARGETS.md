# Targets

Each target is an output format for one CLI agent. Targets read the same
parsed `Workpath` and write files under an output directory. All targets
are **idempotent**: re-running into the same out-dir produces the same tree.

These are compiler formats, not the list of CLIs selectable in the PrAImate
GUI. Run `wpc targets` for the authoritative installed list.

## `claude` — Claude Code skill

```
<out>/.claude/skills/<name>/SKILL.md
<out>/.claude/skills/<name>/scripts/<tool>.sh
<out>/.claude/agents/<name>__<agent>.md
```

`SKILL.md` carries the YAML frontmatter Claude Code's skill loader expects:

```yaml
---
name: <kebab-case-name>
description: <one-line summary>
---
```

The workpath name is kebab-cased on emit (underscores and spaces become
hyphens) because Claude Code's skill-name validator rejects underscores.

Tool scripts are copied into `scripts/` next to `SKILL.md`. Claude Code can
invoke them via the Bash tool using a relative path from the skill dir.

Subagents go in the sibling `.claude/agents/` tree, **namespaced** as
`<workpath>__<agent>.md` so two workpaths can each define a `triage`
subagent without colliding.

### Limitations

- Claude Code skill descriptions must be one line. Multi-line descriptions
  are passed through quoted; they'll work but render awkwardly.
- The `tools` allowlist on an agent is emitted as a YAML inline list. If
  your tool names contain special YAML characters, override them.

## `mika` — legacy mika-code workpath

```
<out>/modules/<name>/module.md
<out>/modules/<name>/playbook.md         (if non-empty)
<out>/modules/<name>/rules.md            (if non-empty)
<out>/modules/<name>/tools/<tool>.sh
<out>/modules/<name>/agents/<agent>.md
```

This is the layout the Go `internal/module` loader expects. `module.md`
prepends a `# <name>` heading and a `> <description>` blockquote to the
mission body so the file is readable on its own.

Agent files preserve their original frontmatter if present; otherwise wpc
synthesizes one (`name`, `description`, optional `tools`).

## `cursor` — Cursor rule

```
<out>/.cursor/rules/<name>.mdc
```

Single `.mdc` file with the frontmatter Cursor expects:

```yaml
---
description: <one-line summary>
globs:
alwaysApply: false
---
```

### Limitations

Cursor has no native concept of agent-callable tools or subagents. Both
are inlined into the body as markdown bullet lists, so the agent at least
*knows* they exist. Scripts are NOT copied — if you need them on disk,
also compile to `generic` or `mika`, or keep the source dir alongside.

## `codex` — Codex CLI / AGENTS.md-compatible hosts

```
<out>/AGENTS.md
<out>/AGENTS.assets/tools/<tool>.sh
<out>/AGENTS.assets/agents/<agent>.md
```

The markdown file inlines mission/playbook/rules and lists tools/agents; the
actual scripts and subagent prompts live in `AGENTS.assets/` so a capable host
can read or invoke them by relative path. Codex CLI loads project and user
`AGENTS.md` instructions. Other hosts may support the project file but use
different user-level paths.

### Limitations

- This compiler format lists subagent prompt files in `AGENTS.md`; it does
  not register native subagent processes. Whether the host can delegate
  execution depends on that host. PrAImate Workers uses its own coordinator.
- `AGENTS.md` will be overwritten on re-compile. If you have a
  hand-authored `AGENTS.md`, compile to a scratch dir
  (`--out /tmp/x`) and merge manually, or use the `generic` target
  and append.

## `generic` — single-file AGENTS.md-style markdown

```
<out>/<name>.md
<out>/<name>.assets/tools/<tool>.sh
<out>/<name>.assets/agents/<agent>.md
```

The single markdown file inlines mission, playbook, rules, and lists of
tools/agents — suitable for any CLI agent that reads an `AGENTS.md` or
similar instruction file, or for hand-pasting into a system prompt.

When tools/agents exist, their source files are copied to a sibling
`<name>.assets/` directory so the agent can shell out to them by path.

## Adding a new target

Each target is a single file in `pkg/targets/`. To add one (say, `opencode`):

1. Create `pkg/targets/opencode.go` implementing the `Target` interface:

    ```go
    type opencodeTarget struct{}
    func (opencodeTarget) Name() string { return "opencode" }
    func (opencodeTarget) Description() string { return "..." }
    func (opencodeTarget) Compile(wp *workpath.Workpath, outDir string) error { ... }
    ```

2. Register it in `pkg/targets/targets.go`'s `init()`:

    ```go
    Register(&opencodeTarget{})
    ```

3. Add a test in `pkg/targets/targets_test.go` (use the `byo` fixture).

That's it — the CLI picks it up automatically.
