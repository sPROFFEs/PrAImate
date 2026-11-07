#!/usr/bin/env bash
# Shared by build/publication jobs; never publishes or changes Git refs.
set -euo pipefail
cd "$(dirname "$0")/.."
edition="${REQUESTED_EDITION:-all}"
release_version="${REQUESTED_VERSION:-}"
case "${GITHUB_REF:-}" in
  refs/tags/server-v*) edition=server; release_version="${GITHUB_REF#refs/tags/server-v}" ;;
  refs/tags/v*) edition=all; release_version="${GITHUB_REF#refs/tags/v}" ;;
esac
case "$edition" in
  desktop) prefix=v; dist=dist ;;
  all) prefix=v; dist=dist/release ;;
  server) prefix=server-v; dist=dist/server ;;
  *) echo 'invalid release edition (use all, desktop or server)' >&2; exit 2 ;;
esac
if [[ -z "$release_version" ]]; then
  release_version="$(sed -n 's/^var Current = "\([^"]*\)"/\1/p' internal/version/version.go)"
fi
if [[ ! "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo 'invalid release version' >&2; exit 2
fi
printf 'VERSION=%s\nEDITION=%s\nTAG=%s%s\nDIST=%s\n' "$release_version" "$edition" "$prefix" "$release_version" "$dist"
