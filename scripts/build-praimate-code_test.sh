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
    if [ -n "${PRAIMATE_TEST_WORKSPACE_RECORD:-}" ]; then
      printf '%s\n' "$PWD" > "$PRAIMATE_TEST_WORKSPACE_RECORD"
    fi
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
    : > dist/test/bin/opencode.exe
    chmod +x dist/test/bin/opencode dist/test/bin/opencode.exe
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

cat > "$TEST_TMP/bin/go" <<'EOF'
#!/usr/bin/env bash
case "$*" in
  'env GOOS') printf 'windows\n' ;;
  'env GOARCH') printf 'amd64\n' ;;
  *) exit 1 ;;
esac
EOF
cat > "$TEST_TMP/bin/cygpath" <<'EOF'
#!/usr/bin/env bash
test "$1" = -u
printf '%s\n' "$2"
EOF
chmod +x "$TEST_TMP/bin/go" "$TEST_TMP/bin/cygpath"
mkdir -p "$TEST_TMP/runner"
PATH="$TEST_TMP/bin:$PATH" RUNNER_TEMP="$TEST_TMP/runner" \
  PRAIMATE_TEST_WORKSPACE_RECORD="$TEST_TMP/workspace" \
  PRAIMATE_BUILD_DIR='' XDG_CONFIG_HOME="$TEST_TMP/long-windows-user-config-directory" OUT="$TEST_TMP/windows-out" \
  bash "$REPO_ROOT/scripts/build-praimate-code.sh" >/dev/null
test -x "$TEST_TMP/windows-out/praimate-code.exe"
workspace="$(cat "$TEST_TMP/workspace")"
case "$workspace" in
  "$TEST_TMP/runner/"*) ;;
  *) echo "Windows CI build used the long user profile instead of runner temp: $workspace" >&2; exit 1 ;;
esac
test "${#workspace}" -lt "$((${#TEST_TMP} + 30))"
echo "praimate-code Windows build path: ok"
