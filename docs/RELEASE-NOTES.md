# PrAImate 1.3.1

**[Install Desktop, Server or Android](https://github.com/sPROFFEs/PrAImate/blob/v1.3.1/docs/INSTALLATION.md)**:
platform downloads, edition selection, first startup and device pairing.

This patch improves Android terminal input, saved-session restoration and device
management in the Server interface.

- Add a standard Android text field and **Send ↵** button for Code terminals.
  Unicode input and multiline text use the existing PTY connection, including
  bracketed paste when supported by the running terminal application.
- Keep each terminal's draft until delivery is acknowledged. Failed or
  unconfirmed commands are never retried automatically. Ownership errors show
  **Take control**, followed by an explicit send.
- Prevent a restored terminal from stealing focus from the mobile text field.
  Fit the terminal above the text controls as the available viewport changes.
- Show **Connecting…** while restoring a saved server session, avoiding the
  brief pairing-form flash when reopening views.
- Hide desktop Detach controls in mobile Code, Chats and Workers. Studio retains
  its Android workspace activities and Back navigation. Device roles and
  permissions are unchanged.
- Remove revoked devices from both Server access lists after refreshing, while
  preserving their internal revocation records.

This combined release is built from `server`; `main` remains Desktop-only.
Choose `praimate-<platform>` for Desktop, `praimate-server-<platform>` for Server,
and `praimate-mobile-preview.apk` for Android. Desktop and Server archives remain
self-contained; shared CLI, PrAImate Code and Graphify standalone assets are
published once per platform. `SHA256SUMS` covers every published asset.

The APK keeps the persistent preview signer and `com.praimate.mobile.debug`
application ID, supporting updates from the previous preview without re-pairing.

Verification includes automated web/transport tests, Android JVM tests and lint,
and a mobile-browser test against a real Core PTY for Unicode delivery, ownership
recovery and session restoration. Physical Android keyboard behavior, native
permission/installer flows and real Cloudflare email/PIN sign-in still require
device/account validation; the available VM cannot validate those flows reliably.

See the [Server guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.1/docs/REMOTE-SERVER.md)
and [Android guide](https://github.com/sPROFFEs/PrAImate/blob/v1.3.1/docs/MOBILE-ANDROID.md)
for setup and limitations.
