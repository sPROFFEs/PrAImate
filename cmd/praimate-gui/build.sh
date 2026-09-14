#!/usr/bin/env bash
# Build the PrAImate GUI binary. The frontend must build first because
# the Go binary embeds frontend/dist via //go:embed.
#
# Linux needs webkit2gtk-4.1 dev headers:
#   apt install libwebkit2gtk-4.1-dev libgtk-3-dev
set -euo pipefail
cd "$(dirname "$0")"

(cd frontend && npm install && npm run build)

# Wails REQUIRES the `desktop` + `production` build tags — without them
# the binary compiles but panics at startup ("Wails applications will
# not build without the correct build tags"). On Linux we also select
# the modern webkit (webkit2gtk-4.1) via webkit2_41; Windows ignores
# that tag. macOS is intentionally unsupported.
TAGS="desktop,production"
case "$(uname -s)" in
  Linux) TAGS="$TAGS,webkit2_41" ;;
  MINGW*|MSYS*|CYGWIN*) ;;
  *) echo "PrAImate GUI supports Linux and Windows only." >&2; exit 2 ;;
esac

# Windows icon: go build embeds the checked-in rsrc_windows_*.syso
# automatically (taskbar + titlebar icon). Regenerate after changing
# frontend/src/assets/monke-icon.png with:
#   go run github.com/tc-hib/go-winres@v0.3.3 simply \
#     --icon frontend/src/assets/monke-icon.png \
#     --product-name "PrAImate GUI" --product-version <version>

# Name the output praimate-gui.exe on Windows (Go does NOT auto-append
# .exe when -o is given) and hide the console via -H windowsgui.
EXT=""
LDFLAGS='-s -w'
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) EXT=".exe"; LDFLAGS='-s -w -H windowsgui' ;;
esac

go build -trimpath -buildvcs=false -tags "$TAGS" -ldflags "$LDFLAGS" -o "praimate-gui$EXT" .
echo "built ./praimate-gui$EXT"
