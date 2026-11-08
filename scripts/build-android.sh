#!/usr/bin/env bash
# Build the packaged client separately from desktop/server binaries. Downloads
# are performed by the explicitly invoked Gradle build, never by the installer.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT="$(pwd)"
VARIANT=Debug
APP_VERSION="${VERSION:-$(cat mobile/android/VERSION)}"
for arg in "$@"; do
  case "$arg" in
    --release) VARIANT=Release ;;
    --help|-h) echo 'Usage: scripts/build-android.sh [--release]'; exit 0 ;;
    *) echo "Unknown argument: $arg" >&2; exit 2 ;;
  esac
done
if [[ -n "${JAVA_HOME:-}" ]]; then export PATH="$JAVA_HOME/bin:$PATH"; fi
command -v java >/dev/null || { echo 'JDK 17+ is required. Set JAVA_HOME; no tools were installed.' >&2; exit 1; }
if [[ -z "${ANDROID_HOME:-${ANDROID_SDK_ROOT:-}}" ]]; then echo 'Set ANDROID_HOME to an Android SDK with platform android-36 and build-tools 35.0.0 or newer.' >&2; exit 1; fi
GRADLE_BIN="${PRAIMATE_ANDROID_GRADLE:-gradle}"
command -v "$GRADLE_BIN" >/dev/null || { echo 'Gradle 8.13 is required. Set PRAIMATE_ANDROID_GRADLE to its executable.' >&2; exit 1; }
ASSETS="$ROOT/mobile/android/app/src/main/assets/web"
(cd cmd/praimate-gui/frontend && npm run build -- --base=/app/web/ --outDir="$ASSETS" --emptyOutDir)
"$GRADLE_BIN" -p "$ROOT/mobile/android" --no-daemon "-PpraimateVersion=$APP_VERSION" "test${VARIANT}UnitTest" "lint${VARIANT}" "assemble${VARIANT}"
mkdir -p dist/android
if [[ "$VARIANT" = Debug ]]; then
  cp mobile/android/app/build/outputs/apk/debug/app-debug.apk dist/android/praimate-mobile-debug.apk
else
  cp mobile/android/app/build/outputs/apk/release/app-release-unsigned.apk dist/android/praimate-mobile-release-unsigned.apk
fi
echo "Android build ready in $ROOT/dist/android (release APKs require your signing key)."
