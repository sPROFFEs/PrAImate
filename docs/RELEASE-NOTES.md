# PrAImate 1.3.10

[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.10/docs/INSTALLATION.md).

- PrAImate CLI keeps responding while the model is streaming, even on turns
  longer than three minutes. A stalled stream still times out, and the caller
  can set a deadline or cancel it. Thanks to @phoenixsite for the fix in #72
  for #71.
- Return to a running chat after switching sections to see its live activity and
  stop the current turn. The composer stays locked until the turn ends, then the
  saved reply appears automatically. #74
- Document chat folds tool calls, reasoning and progress into an activity panel
  you can expand. Permission requests remain visible. #73

Desktop and Server packages include **Linux ARM64/aarch64**. Shared standalone
tools appear once per platform. The signed Android preview is **1.3.10**.
`SHA256SUMS` covers the release artifacts, including the Windows compatibility
aliases.
