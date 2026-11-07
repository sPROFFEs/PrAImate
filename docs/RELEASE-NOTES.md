# PrAImate 1.2.27

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/main/docs/INSTALLATION.md)**:
platform downloads, edition selection, first startup, pairing and separate profiles.

This release includes Desktop, the optional Server edition and an installable
Android preview, built from the `server` branch. `main` remains Desktop-only.
Desktop and Server archives remain self-contained. Shared native
CLI, PrAImate Code and Graphify standalone assets are published once per platform;
there are no separate Server copies of these tools.

- Server provides the shared workspace, conversations, Studio, terminal streams,
  workers, files, device pairing and administration through authenticated HTTPS.
- Optional Cloudflare Tunnel setup installs a dedicated per-user connector from
  a user-provided token. Cloudflare Access email/PIN sign-in is supported by the
  Android client without exposing its native bridge or device bearer to login pages.
- The Android preview uses the desktop interface with mobile navigation and an
  encrypted device vault. It can check GitHub releases manually or once daily
  on opening, download an update over HTTPS, verify SHA-256, package identity and
  its signing certificate, then hand installation to Android with user confirmation.
- Edition-aware installers and updates accept combined releases while refusing
  to install a Desktop archive over Server or vice versa.

Use `praimate-<platform>` for Desktop, `praimate-server-<platform>` for Server,
and `praimate-mobile-preview.apk` for Android. The APK uses a persistent preview
signer and the `com.praimate.mobile.debug` application ID, retaining compatibility
with the local preview builds. `SHA256SUMS` covers every published asset.

Automated Go, browser, distribution, Android JVM and lint checks run before
publication. Server/mobile are available for testing: actual Android device
permissions, native file/installer flows and the real Cloudflare email/PIN flow
still need device/account validation. The VM's software emulator was not usable
for those checks. Existing desktop workflows do not require the remote module.

See `docs/REMOTE-SERVER.md` and `docs/MOBILE-ANDROID.md` for setup and limitations.
