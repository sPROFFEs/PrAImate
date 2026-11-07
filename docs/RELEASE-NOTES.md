# PrAImate 1.3.2

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.2/docs/INSTALLATION.md)**.

This release adds optional system startup for Server profiles.

- Configure **Server → System startup** on the host to install, enable/disable,
  start, stop or remove its background service and inspect bounded logs.
- Start before desktop login using systemd user services with lingering on
  Linux, a boot-triggered password-logon task on Windows, or a system LaunchDaemon
  under the profile owner on macOS. Windows uses account credentials with network
  access rather than S4U or interactive-only logon.
- Require explicit unattended-unlock consent. Windows protects the database
  credential with DPAPI and a private ACL. Linux/macOS use a private owner-readable
  service password file; setup describes this storage before enabling it.
  Startup files are excluded from Git backups; removal revokes the credential.
- Optionally supervise the configured Cloudflare Tunnel with the headless Server,
  avoiding a connector that waits for graphical login. Remove a separate connector
  service before switching to this managed lifetime.
- Connect the host GUI to the existing background Core. Its bounded native
  transport carries uploads, downloads and live events without exposing the local
  owner capability to JavaScript. Closing the client leaves the service running.
- Provide a host recovery screen with status, logs and foreground recovery when
  the service is unavailable. Profile leases prevent duplicate owners.
- Preserve accepted API operations and device revocation state across listener
  restarts; refresh the host connection descriptor after listener address changes.
- Keep desktop listener auto-start separate from OS startup. Browser/Android
  administrators can inspect startup status/logs; OS authorization stays on host.

The [startup guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.2/docs/SERVER-STARTUP.md)
explains account requirements, credential storage, handoff, updates and rollback.
Third-party CLI vaults or encrypted volumes requiring login are not unlocked by
PrAImate. Tests do not install persistent services; native authorization dialogs
and full reboot behavior require validation on each target OS.

This combined release is built from `server`; `main` remains Desktop-only.
Choose `praimate-<platform>` for Desktop, `praimate-server-<platform>` for Server,
and `praimate-mobile-preview.apk` for Android. Application archives remain
self-contained; shared standalone tools are published once per platform.
`SHA256SUMS` covers every published artifact. The APK retains its persistent
preview signer and application ID for updates over previous previews.
