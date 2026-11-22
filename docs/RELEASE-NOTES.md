# PrAImate 1.3.8

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.8/docs/INSTALLATION.md)**.

Desktop, Server and the Android preview are **1.3.8**. This release resolves
#55, #56, #59, #61, #63 and #65 and incorporates the reviewed contributions from
PRs #57, #58, #60, #62, #64, #66, #67, #68, #69 and #70. Thanks to
**phoenixsite** for these contributions.

- Render Worker results and run summaries as escaped Markdown in Desktop,
  Server, Android and Studio. Keep diffs and raw activity literal.
- Retain the selected Studio worker run across section changes and webview
  recreation. Follow-up instructions return to live activity on the new attempt.
- Keep the worker activity scroll position during streaming and follow the
  latest event when enabled. Keep execution-attempt selectors open during
  streaming without delaying task status or attempt-list changes.
- Fold long Studio follow-up instructions into expandable previews and render
  reasoner replies as Markdown.
- Show a Studio sidebar badge for chats and worker runs that finish while the
  sidebar is hidden; clear it when the sidebar is viewed.
- Reopen recent Studio projects with their last CLI, model, agent and tool
  permissions. Legacy entries remain readable. Configure projects individually
  or remove them from history without changing their folders. Preserve explicit
  Safe permissions when reopening through Server or Android.
- Find text in GUI chats with **Find** or **Ctrl/Cmd+F**, including across inline
  Markdown. Navigate matches with Enter/Shift+Enter; finding a folded action
  reveals it. Limit highlighting work for very large histories.
- Add a persisted **Hide activity / Show activity** toggle to GUI and detached
  chats. Hidden actions remain expandable and are never removed from history.
- Update the bundled Studio extension to **0.7.5**. Restart PrAImate and reopen
  Studio after updating to load the new integration.

Validation covers Go Studio/history and race checks, native extension tests,
frontend regressions, Server project permissions/history, production frontend
builds and offline release/edition contracts. Real Firefox DOM checks exercise
both themes, live scroll and selectors, Markdown, saved selection and chat find
inside folded activity. Native editor and physical Android smoke tests remain
device-dependent.

Application archives contain their edition's binaries, including **Linux
ARM64/aarch64** Desktop and Server packages. Shared standalone tools are
published once per platform. The `praimate-desktop-windows-*` ZIPs remain
byte-identical compatibility aliases for old Desktop updaters. `SHA256SUMS`
covers every artifact; `ANDROID-VERSION` records the APK version.
