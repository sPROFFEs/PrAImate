# PrAImate 1.3.9

[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.9/docs/INSTALLATION.md).

- Install REA 6.10.1 from **CLIs → Managed tools**. PrAImate installs it in
  the profile, verifies its local MCP connection and registers it in **MCP**.
  Select REA in a managed session to analyse local artifacts and saved captures.
  The bundled profile offers offline analysis tools. The package requires a
  compatible Node.js runtime and npm.
- Use the same Graphify executable in the CLI catalogue, Studio knowledge
  indexing and managed knowledge queries. The bundled build takes precedence,
  and a working alternative is used when its version probe fails. The pinned
  uv method can repair an existing installation.
- Install ScrapeGraphAI in a Python 3.12 environment managed by PrAImate, and
  show the library and SDK versions when detection succeeds.
- Report gstack's actual version and check Git, Bash and Bun before setup.
  Keep Studio visible as installed while its extension has an update pending.
  Installation failures and probe errors are visible in **CLIs → Managed tools**.

Desktop and Server packages include **Linux ARM64/aarch64**. Shared standalone
tools appear once per platform. The signed Android preview is **1.3.9**.
`SHA256SUMS` covers the release artifacts, including the Windows compatibility
aliases.
