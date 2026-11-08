# Mobile layout regression checks

`npm run test:mobile` checks navigation, dialogs, terminal input, composer keys
and visual-viewport handling without a browser.

The optional browser suite renders the real Svelte application, CodeMirror and
xterm against synthetic backend responses. It does not connect to a PrAImate
Server or read a user profile. With Node 22+ and Firefox installed, start these
in separate terminals from the frontend directory:

```sh
npm run dev -- --host 127.0.0.1 --port 5187
```

```sh
mkdir -p /tmp/praimate-mobile-layout-firefox
firefox --headless --no-remote --new-instance --remote-debugging-port=9234 --profile /tmp/praimate-mobile-layout-firefox about:blank
```

Then run `npm run test:mobile-layout`. Use a separate Firefox profile, with no
other active WebDriver BiDi session. `BIDI_URL`, `MOBILE_TEST_URL` and
`MOBILE_TEST_OUTPUT` can override the connection, fixture URL and screenshot
directory. `MOBILE_TEST_SCOPE=workspaces` runs only the terminal and editor checks.

The suite checks 14 sections at phone, landscape and tablet sizes in both themes,
conversation input while the viewport shrinks, dialog dismissal with Back,
worker filters, terminal control placement and UTF-8 delivery, and the Studio
and agent editor panels. Screenshots are written to
`/tmp/praimate-mobile-layout-evidence` by default. This browser check complements
Android device testing; it cannot reproduce native IME, system insets or WebView
behavior on every device.
