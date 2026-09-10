# P6 — Terminal native delivery

P6 adds a deliberately narrow native-skill transport. It is not an assertion
that every installed CLI understands skills.

## Capability contract

An adapter must explicitly implement `core.NativeSkillAdapter` and expose a
characterized `NativeSkillCapabilities` value. The required minimum is
`native_skill_discovery` plus `scoped_skill_root`. A CLI name, an installed
binary, a checkbox, or a copied `SKILL.md` never supplies that evidence.

For each supported version, the adapter test must prove its discovery root and
record whether it can enforce a strict selected set, emit activation events,
emit resource-read events, hot-update a session, or expose the full request.
The current built-in interactive adapters intentionally declare none of these
native capabilities. Their terminal route therefore gives an explicit
`native_skill_unsupported` fallback when an effective v2 selection exists.

## Per-session materialization protocol

`core.MaterializeNativeSkills` accepts only a pinned, host-resolved compatible
selection. It re-reads every package through `HostSkillStore`, verifies the
version digest, and writes a private directory below:

```
<PRAIMATE_HOME>/native-skill-sessions/<random>/
  manifest.json
  skills/<opaque-ref-digest>/SKILL.md
  skills/<opaque-ref-digest>/<resource>
```

The child receives only `PRAIMATE_SKILL_ROOT` and
`PRAIMATE_SKILL_MANIFEST`. It receives no inline skill payload at the same
time. Package executable intent is preserved as file mode only; materializing
a script does not approve or execute it.

This protocol never writes a project `AGENTS.md`/`CLAUDE.md`, global CLI
configuration, or credentials. Cleanup removes only files whose content hash
still matches the materialized manifest. If a file has changed, cleanup leaves
the directory intact and reports preservation instead of deleting the edit.

## Observable state

Materialization means only that the selected bytes were made visible in the
private root. It does **not** mean that the CLI read or used them. Unless the
adapter emits reliable resource-read events, the receipt is
`coverage=unknown` and the UI-facing state is `materialized; read unknown`.
Native private prompt/history remains unobservable unless a verified adapter
can provide full-request evidence.

## Terminal behavior

Before a Terminal starts, PrAImate resolves the frozen application/agent v2
selection again. If it cannot guarantee native delivery, the launch fails
before project-scoped MCP/provider writes. A strict selection also fails when
the adapter cannot exclude external global/project discoveries. A non-hot
adapter receives `requires_new_session`; it must start a new session after a
selection change. P6 does not claim that resume reloads it.

The fallback project-only shape has a keyed lease primitive. It rejects two
different active selections for the same canonical working directory rather
than letting them overwrite each other. Current adapters do not use this
shape because the safer scoped-root route is required.

## Acceptance evidence

`internal/core/native_skills_test.go` uses a fake adapter, not a CLI-name
assumption. It proves: selected pinned content is the only materialized entry;
there is exactly one native transport and no inline payload; unread state
stays unknown; strict mode blocks global discovery; modified materialized files
survive cleanup; and conflicting project fallback leases block.
