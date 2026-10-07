#!/usr/bin/env bash

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TEST_TMP="$(mktemp -d)"
trap 'rm -rf "$TEST_TMP"' EXIT
mkdir -p "$TEST_TMP/bin"

cat > "$TEST_TMP/bin/bun" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

case "$1" in
  install)
    for asset in apple-touch-icon-v3.png favicon-v3.ico site.webmanifest social-share.png; do
      test -f "packages/app/public/$asset"
      test ! -L "packages/app/public/$asset"
    done
    ;;
  run)
    grep -q 'praimate:host-tls' src/provider/provider.ts
    test -f src/provider/praimate-host-tls.ts
    mkdir -p dist/test/bin
    : > dist/test/bin/opencode
    chmod +x dist/test/bin/opencode
    ;;
  */praimate-code-tls.mjs)
    exec node "$@"
    ;;
esac
EOF
chmod +x "$TEST_TMP/bin/bun"

cat > "$TEST_TMP/bin/df" <<'EOF'
#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf 'testfs 20971520 0 20971520 0%% /tmp\n'
EOF
chmod +x "$TEST_TMP/bin/df"

PATH="$TEST_TMP/bin:$PATH" XDG_CONFIG_HOME="$TEST_TMP/config" OUT="$TEST_TMP/out" \
  bash "$REPO_ROOT/scripts/build-praimate-code.sh" >/dev/null

test -x "$TEST_TMP/out/praimate-code"
echo "praimate-code public assets: ok"
