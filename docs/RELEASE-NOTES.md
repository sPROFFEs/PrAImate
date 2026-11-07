# PrAImate 1.3.0

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.0/docs/INSTALLATION.md)**:
platform downloads, edition selection, first startup and device pairing.

This minor release brings Desktop, Server and Android into the 1.3 series.

The Server edition has a visible **SERVER** label and a dedicated **Server**
page, also linked from **Settings → Configure Server**. Configure remote access
without environment variables or terminal commands.

- Configure the listen address/port, TLS certificate and key paths, allowed
  browser origins, and automatic startup after database unlock. Settings are
  stored in the encrypted database.
- Save settings independently of applying them. Start, stop or restart the
  listener while local chats, workers and native terminals keep running. A failed
  bind restores the previous listener configuration and shows the error.
- Manage Cloudflare Tunnel, generate pairing codes for direct or tunnel
  connections, and list/revoke paired devices from the same page. Saving
  connector settings respects a stopped listener and the startup preference.
- Listener restarts preserve device authentication and the bootstrap verifier.
  Remote restart requests retain their completed operation receipt.
- Fix pairing-code validation in the browser and APK so all eight-digit codes,
  including codes with leading zeroes, are accepted. Explain administrator pairing
  and the optional bootstrap token on the connection screen.
- Let the Server configuration page fill the available window width and resize
  with desktop windows while retaining mobile layouts.
- Fix browser connections failing with `Illegal invocation` when calling native
  `fetch`. The shared Server page supports light/dark themes and mobile layouts;
  remote administration is restricted to administrator devices.

This combined release is built from `server`; `main` remains Desktop-only.
Choose `praimate-<platform>` for Desktop, `praimate-server-<platform>` for Server,
and `praimate-mobile-preview.apk` for Android. Desktop and Server archives remain
self-contained; shared CLI, PrAImate Code and Graphify standalone assets are
published once per platform. `SHA256SUMS` covers every published asset.

The APK keeps the persistent preview signer and `com.praimate.mobile.debug`
application ID, supporting updates from the previous preview without re-pairing.
Server/mobile remain available for testing. Native Android permission/installer
flows and real Cloudflare email/PIN sign-in still require device/account
validation; the VM's software emulator cannot validate those flows reliably.

See the [Server guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.0/docs/REMOTE-SERVER.md)
and [Android guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.0/docs/MOBILE-ANDROID.md)
for setup and limitations.
