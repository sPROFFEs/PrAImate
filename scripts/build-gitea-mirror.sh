#!/usr/bin/env bash
# build-gitea-mirror.sh — Builds an internal Gitea release mirror of PrAImate.
#
# Copies the PrAImate codebase to an isolated directory, adapts internal URLs
# (updating updater, install scripts, and API URLs to point to the local Gitea instance),
# cross-compiles all platform release packages, and generates SHA256 checksums.
#
# Usage:
#   bash scripts/build-gitea-mirror.sh [OPTIONS]
#
# Options:
#   --host <url>       Gitea base URL (default: https://git.jtsec.local)
#   --repo <owner/re>  Gitea repo path (default: lab/PrAImate)
#   --out <dir>        Output directory (default: /tmp/praimate-gitea-release)
#   --version <ver>    Override build version (default: from internal/version/version.go)
#   --skip-build       Only prepare source in out dir without compiling binaries
#   -h, --help         Show this help message

set -euo pipefail

GITEA_HOST="${GITEA_HOST:-https://git.jtsec.local}"
GITEA_REPO="${GITEA_REPO:-lab/PrAImate}"
OUT_DIR="${OUT_DIR:-/tmp/praimate-gitea-release}"
VERSION=""
SKIP_BUILD=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --host)       GITEA_HOST="$2"; shift 2 ;;
    --host=*)     GITEA_HOST="${1#--host=}"; shift ;;
    --repo)       GITEA_REPO="$2"; shift 2 ;;
    --repo=*)     GITEA_REPO="${1#--repo=}"; shift ;;
    --out)        OUT_DIR="$2"; shift 2 ;;
    --out=*)      OUT_DIR="${1#--out=}"; shift ;;
    --version)    VERSION="$2"; shift 2 ;;
    --version=*)  VERSION="${1#--version=}"; shift ;;
    --skip-build) SKIP_BUILD=1; shift ;;
    -h|--help)
      sed -n '2,/^set -/p' "$0" | tr -d '#'
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      exit 1
      ;;
  esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
if [[ -z "$VERSION" ]]; then
  VERSION=$(grep 'var Current =' "$REPO_ROOT/internal/version/version.go" | sed -E 's/.*"([^"]+)".*/\1/')
fi

echo "============================================================"
echo " PrAImate Gitea Release Mirror Builder"
echo "============================================================"
echo " Version:    $VERSION"
echo " Gitea Host: $GITEA_HOST"
echo " Gitea Repo: $GITEA_REPO"
echo " Output Dir: $OUT_DIR"
echo "============================================================"

SRC_DIR="$OUT_DIR/src"
DIST_DIR="$OUT_DIR/dist"

rm -rf "$SRC_DIR"
mkdir -p "$SRC_DIR" "$DIST_DIR"

echo "→ Copying source to isolated directory..."
rsync -a --exclude='.git' --exclude='dist' --exclude='node_modules' --exclude='.svelte-kit' "$REPO_ROOT/" "$SRC_DIR/"

echo "→ Modifying internal version URLs for Gitea..."
VERSION_GO="$SRC_DIR/internal/version/version.go"
if [[ -f "$VERSION_GO" ]]; then
  sed -i "s|const ForgeBaseURL = \".*\"|const ForgeBaseURL = \"$GITEA_HOST\"|g" "$VERSION_GO"
  sed -i "s|const Repo = \".*\"|const Repo = \"$GITEA_REPO\"|g" "$VERSION_GO"
  sed -i "s|const ReleaseLatestAPIURL = \".*\"|const ReleaseLatestAPIURL = \"$GITEA_HOST/api/v1/repos/$GITEA_REPO/releases/latest\"|g" "$VERSION_GO"
fi

echo "→ Modifying install scripts for Gitea..."
INSTALL_SH="$SRC_DIR/scripts/install.sh"
if [[ -f "$INSTALL_SH" ]]; then
  sed -i "s|REPO=\".*\"|REPO=\"$GITEA_REPO\"|g" "$INSTALL_SH"
  sed -i "s|GITHUB_URL=\".*\"|GITHUB_URL=\"$GITEA_HOST\"|g" "$INSTALL_SH"
  sed -i "s|RELEASE_API_URL=\".*\"|RELEASE_API_URL=\"$GITEA_HOST/api/v1/repos/$GITEA_REPO/releases/latest\"|g" "$INSTALL_SH"
fi

INSTALL_PS1="$SRC_DIR/scripts/install.ps1"
if [[ -f "$INSTALL_PS1" ]]; then
  sed -i "s|\[string\] \$Repo = \".*\"|\[string\] \$Repo = \"$GITEA_REPO\"|g" "$INSTALL_PS1"
  sed -i "s|\[string\] \$GitHubUrl = \".*\"|\[string\] \$GitHubUrl = \"$GITEA_HOST\"|g" "$INSTALL_PS1"
fi

if [[ $SKIP_BUILD -eq 1 ]]; then
  echo "✓ Source prepared in $SRC_DIR (build skipped)."
  exit 0
fi

echo "→ Compiling release binaries and archives in $SRC_DIR..."
(
  cd "$SRC_DIR"
  VERSION="$VERSION" bash scripts/build.sh
)

echo "→ Copying release artifacts to $DIST_DIR..."
cp -r "$SRC_DIR/dist/"* "$DIST_DIR/"

echo ""
echo "============================================================"
echo " Build Complete! Artifacts staged in: $DIST_DIR"
echo "============================================================"
echo ""
echo "Checksums (SHA256):"
if [[ -f "$DIST_DIR/SHA256SUMS" ]]; then
  cat "$DIST_DIR/SHA256SUMS"
fi

echo ""
echo "============================================================"
echo " Gitea Upload Instructions"
echo "============================================================"
echo "To publish this release on your Gitea instance:"
echo ""
echo "1. Push tag to Gitea:"
echo "   git tag -a $VERSION -m \"PrAImate $VERSION\""
echo "   git push $GITEA_HOST/$GITEA_REPO.git $VERSION"
echo ""
echo "2. Create release via Gitea API (or tea CLI):"
echo "   curl -X POST \"$GITEA_HOST/api/v1/repos/$GITEA_REPO/releases\" \\"
echo "     -H \"Authorization: token \$GITEA_TOKEN\" \\"
echo "     -H \"Content-Type: application/json\" \\"
echo "     -d '{\"tag_name\":\"$VERSION\",\"name\":\"PrAImate $VERSION\",\"body\":\"Release $VERSION for internal mirror.\"}'"
echo ""
echo "3. Upload assets to Gitea release:"
echo "   for file in $DIST_DIR/*.tar.gz $DIST_DIR/*.zip $DIST_DIR/*.sha256 $DIST_DIR/SHA256SUMS; do"
echo "     [ -f \"\$file\" ] || continue"
echo "     curl -X POST \"$GITEA_HOST/api/v1/repos/$GITEA_REPO/releases/<release_id>/assets?name=\$(basename \$file)\" \\"
echo "       -H \"Authorization: token \$GITEA_TOKEN\" \\"
echo "       -H \"Content-Type: application/octet-stream\" \\"
echo "       --data-binary \"@\$file\""
echo "   done"
echo "============================================================"
