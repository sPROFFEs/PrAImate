#!/usr/bin/env bash
# Build the pinned, CPU-only speech server used by internal/voice. Release
# publication is a separate operator step; this script only creates packages.
set -euo pipefail

SOURCE="${1:?provide whisper.cpp source directory}"
OUT="${2:?provide output directory}"
ARCH="${3:?provide amd64 or arm64}"
COMMIT=927cfce34f31707e17f2bff35c349632fb9e2c3a
if [ "$(uname -s)" != Darwin ]; then
  echo 'This runtime requires a native macOS build host.' >&2
  exit 2
fi
case "$ARCH:$(uname -m)" in
  arm64:arm64) CMAKE_ARCH=arm64 ;;
  amd64:x86_64) CMAKE_ARCH=x86_64 ;;
  *) echo 'Build architecture does not match the native host.' >&2; exit 2 ;;
esac
if [ "$(git -C "$SOURCE" rev-parse HEAD)" != "$COMMIT" ]; then
  echo 'whisper.cpp checkout does not match the pinned source commit.' >&2
  exit 2
fi
mkdir -p "$OUT/package"
SOURCE="$(cd "$SOURCE" && pwd)"
OUT="$(cd "$OUT" && pwd)"
SOURCE_SHA="$(git -C "$SOURCE" archive --format=tar HEAD | shasum -a 256 | cut -d' ' -f1)"

# Static linking avoids dependencies on Homebrew or a developer's library path.
# Voice currently selects CPU inference (-ng). Disable host-specific ISA flags
# and external OpenMP/Accelerate/Metal dependencies for macOS 12+ portability.
cmake -S "$SOURCE" -B "$OUT/build" \
  -DCMAKE_BUILD_TYPE=Release -DCMAKE_OSX_ARCHITECTURES="$CMAKE_ARCH" \
  -DCMAKE_OSX_DEPLOYMENT_TARGET=12.0 -DBUILD_SHARED_LIBS=OFF \
  -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_EXAMPLES=ON -DWHISPER_BUILD_SERVER=ON \
  -DGGML_NATIVE=OFF -DGGML_METAL=OFF -DGGML_ACCELERATE=OFF -DGGML_OPENMP=OFF \
  -DGGML_AVX=OFF -DGGML_AVX2=OFF -DGGML_FMA=OFF -DGGML_F16C=OFF
cmake --build "$OUT/build" --config Release --target whisper-server --parallel 3
cp "$OUT/build/bin/whisper-server" "$OUT/package/whisper-server"
cp "$SOURCE/LICENSE" "$OUT/package/LICENSE"
chmod 755 "$OUT/package/whisper-server"
codesign --force --sign - "$OUT/package/whisper-server"
codesign --verify --strict "$OUT/package/whisper-server"
otool -L "$OUT/package/whisper-server" > "$OUT/dependencies.txt"
python3 - "$OUT" "$ARCH" "$SOURCE_SHA" "$COMMIT" <<'PY'
import gzip, hashlib, io, json, pathlib, sys, tarfile

out, arch, source_sha, commit = pathlib.Path(sys.argv[1]), *sys.argv[2:]
for line in (out / 'dependencies.txt').read_text().splitlines()[1:]:
    dependency = line.strip().split(' (', 1)[0]
    if not dependency.startswith(('/usr/lib/', '/System/Library/')):
        raise SystemExit(f'Unbundled runtime dependency: {dependency}')
build = {
    'upstream_commit': commit, 'source_archive_format': 'git archive --format=tar HEAD',
    'source_archive_sha256': source_sha, 'minimum_macos': '12.0',
    'architecture': arch, 'static': True, 'cpu_only': True, 'native_isa': False,
    'code_signature': 'ad-hoc',
}
(out / 'package' / 'BUILD.json').write_text(json.dumps(build, indent=2) + '\n')
filename = f'whisper-cpp-darwin-{arch}-b5130-v1.tar.gz'
with (out / filename).open('wb') as raw, gzip.GzipFile(fileobj=raw, mode='wb', filename='', mtime=0) as gz:
    with tarfile.open(fileobj=gz, mode='w') as archive:
        for path in sorted((out / 'package').iterdir()):
            data = path.read_bytes()
            info = tarfile.TarInfo(path.name)
            info.size, info.mode, info.mtime = len(data), 0o755 if path.name == 'whisper-server' else 0o644, 0
            archive.addfile(info, io.BytesIO(data))
payload = (out / filename).read_bytes()
artifact = {
    'filename': filename, 'size': len(payload), 'sha256': hashlib.sha256(payload).hexdigest(),
    'format': 'tar.gz', 'entry_point': 'whisper-server', 'license_name': 'MIT',
    'license_url': 'https://raw.githubusercontent.com/ggml-org/whisper.cpp/b5130/LICENSE',
    'license_text': (out / 'package' / 'LICENSE').read_text(),
    'upstream_project': 'ggml-org/whisper.cpp', 'upstream_version': 'b5130',
    'upstream_sha256': source_sha,
}
(out / f'{filename}.json').write_text(json.dumps({
    'artifact_id': f'runtime/whisper.cpp/darwin-{arch}/v1',
    'artifact': artifact, 'build': build,
}, indent=2) + '\n')
print(f'Packaged {filename}: {artifact["sha256"]}')
PY
