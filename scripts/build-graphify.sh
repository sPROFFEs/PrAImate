#!/usr/bin/env bash
# Build "praimate-graphify" — a self-contained, zero-dependency
# standalone binary of graphify (graphifyy on PyPI, MIT-ish), the same
# bundling model as PrAImate Code: ship a known-good build as a release
# asset so our RAG feature keeps working even if the user's PATH
# graphify changes/breaks or they don't have uv/Python.
#
# graphify is a pure-Python tool; we freeze it with PyInstaller into one
# executable that carries its own CPython + deps (incl. the openai
# client for the OpenAI / Local-LLM backends). The binary lands at
# $OUT/praimate-graphify (default: dist/<goos>-<goarch>/).
#
# LICENSE / ATTRIBUTION: graphify is third-party; we redistribute a
# version-pinned build under a praimate- prefix and keep the upstream
# notice next to it. We do NOT modify graphify's behavior.
#
# Requires: uv (>=0.4), ~500MB disk, network. PyInstaller can't
# cross-compile — run on each native host (matches build-praimate-code).
#
# Usage:
#   scripts/build-graphify.sh                  # native target → dist/<triplet>/
#   OUT=/some/dir scripts/build-graphify.sh    # custom output dir
#   GRAPHIFY_PIN=0.8.36 scripts/build-graphify.sh   # install from PyPI instead of vendored source

set -euo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

# Keep in sync with installer.GraphifyPinnedVersion.
GRAPHIFY_PIN="${GRAPHIFY_PIN:-0.8.36}"
# Vendored graphify source (the exact 0.8.36 sdist; see third_party/README.md).
# Set GRAPHIFY_PIN to force a PyPI install of that version instead.
VENDORED_GRAPHIFY="$REPO_ROOT/third_party/graphify"

GOOS="$(go env GOOS 2>/dev/null || uname -s | tr '[:upper:]' '[:lower:]')"
GOARCH="$(go env GOARCH 2>/dev/null || echo amd64)"
OUT="${OUT:-$REPO_ROOT/dist/$GOOS-$GOARCH}"
# Ensure OUT is absolute before we cd to $WORK
if [[ "$OUT" != /* ]]; then
  OUT="$REPO_ROOT/$OUT"
fi
EXT=""
[ "$GOOS" = "windows" ] && EXT=".exe"

command -v uv >/dev/null 2>&1 || { echo "error: uv not found on PATH (install from https://astral.sh/uv)"; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
cd "$WORK"

uv venv --python 3.13 .venv
# shellcheck disable=SC1091
if [ -f .venv/bin/activate ]; then
  . .venv/bin/activate
elif [ -f .venv/Scripts/activate ]; then
  . .venv/Scripts/activate
fi
# Prefer the vendored source so the build is self-contained re: graphify
# itself (its own deps still resolve from PyPI). Falls back to PyPI when
# the vendored tree is absent or GRAPHIFY_PIN is overridden explicitly.
GRAPHIFY_TARGET="$VENDORED_GRAPHIFY[openai]"
if command -v cygpath >/dev/null 2>&1; then
  GRAPHIFY_TARGET="$(cygpath -w "$VENDORED_GRAPHIFY")[openai]"
fi

if [ -z "${GRAPHIFY_PIN_OVERRIDE:-}" ] && [ -d "$VENDORED_GRAPHIFY" ]; then
  echo "→ freezing vendored graphify ($GRAPHIFY_TARGET) with PyInstaller"
  uv pip install --index-url=https://pypi.org/simple/ "$GRAPHIFY_TARGET" pyinstaller || {
    echo "→ falling back to PyPI graphifyy[openai]==$GRAPHIFY_PIN"
    uv pip install --index-url=https://pypi.org/simple/ "graphifyy[openai]==$GRAPHIFY_PIN" pyinstaller
  }
else
  echo "→ freezing graphifyy[openai]==$GRAPHIFY_PIN (PyPI) with PyInstaller"
  uv pip install --index-url=https://pypi.org/simple/ "graphifyy[openai]==$GRAPHIFY_PIN" pyinstaller
fi

cat > entry.py <<'EOF'
from graphify.__main__ import main
if __name__ == "__main__":
    main()
EOF

pyinstaller --onefile --name praimate-graphify \
  --collect-all graphify --collect-submodules graphify \
  --collect-submodules openai \
  entry.py

BUILT="dist/praimate-graphify$EXT"
[ -f "$BUILT" ] || { echo "error: PyInstaller produced no binary at $BUILT"; exit 1; }

# Smoke-test in a clean env (no Python on PATH) before shipping.
if [ "$GOOS" != "windows" ]; then
  env -i PATH=/usr/bin:/bin "$PWD/$BUILT" --version >/dev/null 2>&1 \
    || { echo "error: frozen graphify failed its --version smoke test"; exit 1; }
fi

mkdir -p "$OUT"
cp -f "$BUILT" "$OUT/praimate-graphify$EXT"
chmod 755 "$OUT/praimate-graphify$EXT" 2>/dev/null || true
cat > "$OUT/PRAIMATE-GRAPHIFY-NOTICE" <<EOF
praimate-graphify is a version-pinned, self-contained build of graphify
(graphifyy on PyPI), redistributed by PrAImate so its RAG feature has a
known-good fallback independent of the user's Python/uv/PATH.

Pinned version: $GRAPHIFY_PIN
Built with PyInstaller (carries its own CPython + dependencies).

graphify is the work of its authors; PrAImate ships a frozen build
under a praimate- prefix without modifying its behavior.
EOF

echo
echo "✓ built $OUT/praimate-graphify$EXT"
ls -lh "$OUT/praimate-graphify$EXT"
