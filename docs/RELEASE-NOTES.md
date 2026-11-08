# PrAImate 1.3.3

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.3/docs/INSTALLATION.md)**.

This release fixes Linux Server startup and CLI discovery/updates when running
without an interactive shell.

- Generate a valid systemd `WorkingDirectory` value. Version 1.3.2 incorrectly
  quoted the whole path, causing systemd to reject the unit as `bad-setting`.
- Report invalid or unloaded systemd units as needing attention instead of
  showing them ready to start at boot. Add real `systemd-analyze` validation.
- Discover stable NVM/FNM installations, including XDG and custom locations.
  Preserve an active manager runtime, honor default version aliases and sort
  installed versions numerically when a fallback is needed.
- Save the selected stable Node directory and custom manager roots in startup
  configuration, without persisting temporary FNM multishell links or sourcing
  shell configuration files.
- Bind global npm updates/reinstalls to the detected CLI's own Node and prefix.
  Updating a CLI in one Node installation no longer silently targets another.
  The installer does not elevate to modify root-owned packages.
- Exercise CLI discovery and installer regressions in the release workflow.

**After updating from 1.3.2:** reopen the updated Server app, choose **Server →
System startup → Update startup configuration**, then restart/start the
background service. This regenerates its Linux unit and records the stable
runtime paths. Saved Cloudflare settings and paired devices are retained.
See the [startup guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.3/docs/SERVER-STARTUP.md).

Tests cover service-like environments and controlled npm execution; they do not
perform real third-party package updates or enable persistent OS services.
Native authorization and complete boot behavior still require target-OS testing.

This combined release includes Desktop, Server and the signed Android preview.
Application archives are self-contained; shared standalone tools are published
once per platform. `SHA256SUMS` covers all published artifacts. The Android
preview keeps its existing application ID and persistent signer for updates.
`main` remains Desktop-only; Server implementation stays on `server`.
