#!/usr/bin/env python3
"""Stage an APK-only update, retaining the host release's checksums and notes."""
import argparse
import hashlib
import json
from pathlib import Path
import re

APK = "praimate-mobile-preview.apk"
VERSION_MARKER = r"<!-- praimate-android-version:([0-9]+\.[0-9]+\.[0-9]+) -->"
BLOCK = r"<!-- praimate-android-update:start -->.*?<!-- praimate-android-update:end -->"


def stage(release, manifest, apk, version, source, output):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise ValueError("Expected a stable Android version")
    if not re.fullmatch(r"[0-9a-f]{40}", source):
        raise ValueError("Expected the full source commit SHA")
    tag = release["tagName"]
    if release.get("isDraft") or not re.fullmatch(r"(?:server-)?v[0-9]+\.[0-9]+\.[0-9]+", tag):
        raise ValueError("Expected an existing published host release")
    notes = release.get("body") or ""
    markers = re.findall(VERSION_MARKER, notes)
    if len(markers) > 1:
        raise ValueError("Ambiguous Android version in release notes")
    previous = markers[0] if markers else re.sub(r"^(?:server-)?v", "", tag)
    if tuple(map(int, version.split("."))) <= tuple(map(int, previous.split("."))):
        raise ValueError("The Android version must increase")
    if not 0 < apk.stat().st_size <= 64 * 1024 * 1024:
        raise ValueError("APK size is outside the supported update limit")
    lines = manifest.splitlines(keepends=True)
    parsed = []
    for line in lines:
        match = re.fullmatch(r"([0-9a-fA-F]{64})  ([^\r\n]+)\r?\n?", line)
        if not match:
            raise ValueError("Invalid existing release checksum manifest")
        parsed.append(match.group(2))
    if len(set(parsed)) != len(parsed) or parsed.count(APK) != 1:
        raise ValueError("The existing release must contain one preview APK checksum")
    output.mkdir(parents=True, exist_ok=True)
    metadata = f"<!-- praimate-android-version:{version} -->\n"
    (output / "ANDROID-VERSION").write_text(metadata)
    kept = [line for line, name in zip(lines, parsed) if name not in (APK, "ANDROID-VERSION")]
    # Preserve all unrelated entries verbatim, including their order.
    if kept and not kept[-1].endswith("\n"):
        kept[-1] += "\n"
    kept += [f"{hashlib.sha256(apk.read_bytes()).hexdigest()}  {APK}\n",
             f"{hashlib.sha256(metadata.encode()).hexdigest()}  ANDROID-VERSION\n"]
    (output / "SHA256SUMS").write_text("".join(kept))
    block = ("<!-- praimate-android-update:start -->\n" + metadata +
             f"### Android client {version}\n\n"
             "Updated mobile navigation, responsive workspaces and the native server chooser. "
             "Desktop, Server and shared CLI artifacts retain their existing versions.\n\n"
             "Install this APK once manually when upgrading from Android 1.3.3 or earlier. "
             "Subsequent Android updates can be detected independently of the host release version.\n\n"
             f"Android source: [{source[:8]}](https://github.com/sPROFFEs/PrAImate/commit/{source}).\n"
             "<!-- praimate-android-update:end -->")
    starts = notes.count("<!-- praimate-android-update:start -->")
    ends = notes.count("<!-- praimate-android-update:end -->")
    if starts != ends or starts != len(re.findall(BLOCK, notes, re.S)) or starts > 1:
        raise ValueError("Ambiguous Android notes block")
    updated = re.sub(BLOCK, lambda _: block, notes, flags=re.S) if re.search(BLOCK, notes, re.S) else notes.rstrip() + "\n\n" + block + "\n"
    (output / "notes.md").write_text(updated)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--release", type=Path, required=True)
    parser.add_argument("--checksums", type=Path, required=True)
    parser.add_argument("--apk", type=Path, required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    stage(json.loads(args.release.read_text()), args.checksums.read_text(), args.apk,
          args.version, args.source, args.output)
