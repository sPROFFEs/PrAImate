"""Edition distribution tests; all build output stays in temporary fixtures."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parent.parent


class Editions(unittest.TestCase):
    def test_server_installer_resolves_only_published_server_channel(self):
        source = (ROOT / "scripts/install.sh").read_text()
        start = source.index("resolve_latest_tag() {")
        end = source.index("\n}\n", start) + 3
        resolver = source[start:end]
        with tempfile.TemporaryDirectory(prefix="praimate-release-fixture-") as directory:
            payload = Path(directory) / "releases.json"
            prefix = '''set -euo pipefail
EDITION=server
REPO=sPROFFEs/praimate
detect_downloader() { printf curl; }
c_red() { printf '%s\\n' "$*" >&2; }
curl() {
  case "$*" in */releases/latest*) exit 98 ;; esac
  cat "$PRAIMATE_TEST_RELEASES"
}
'''
            script = prefix + resolver + "\nresolve_latest_tag\n"
            env = dict(os.environ, PRAIMATE_TEST_RELEASES=str(payload))
            payload.write_text(json.dumps([
                {"tag_name": "v99.0.0", "body": 'example "tag_name":"server-v99.0.0"'},
                {"tag_name": "server-v1.3.0", "body": "x" * 200000},
                {"tag_name": "server-v1.2.26"},
            ]))
            result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, "server-v1.3.0")
            payload.write_text('[{"tag_name":"v99.0.0"}]')
            result = subprocess.run(["bash", "-c", script], env=env, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("No server release", result.stderr)

    def test_release_tags_select_edition_and_cannot_execute_input(self):
        for ref, requested, expected in [
            ("refs/tags/v1.3.0", "server", "all"),
            ("refs/tags/server-v1.3.0", "desktop", "server"),
            ("refs/heads/server", "server", "server"),
        ]:
            env = dict(os.environ, GITHUB_REF=ref, REQUESTED_EDITION=requested,
                       REQUESTED_VERSION="1.3.0")
            output = subprocess.check_output(["bash", "scripts/release-metadata.sh"], cwd=ROOT, env=env, text=True)
            metadata = dict(line.split("=", 1) for line in output.splitlines())
            self.assertEqual(metadata["EDITION"], expected)
            self.assertEqual(metadata["DIST"], {"server":"dist/server","desktop":"dist","all":"dist/release"}[expected])
            self.assertEqual(metadata["TAG"], ("server-v" if expected == "server" else "v") + "1.3.0")
        env = dict(os.environ, GITHUB_REF="refs/heads/server", REQUESTED_EDITION="server",
                   REQUESTED_VERSION="1.3.0; echo unexpected")
        result = subprocess.run(["bash", "scripts/release-metadata.sh"], cwd=ROOT, env=env, capture_output=True, text=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertNotIn("unexpected", result.stdout)

    def test_builds_keep_packages_and_tags_separate(self):
        with tempfile.TemporaryDirectory(prefix="praimate-editions-") as directory:
            fixture = Path(directory)
            for name in ["scripts", "samples", "docs/assets", "examples", "cmd/praimate-gui/frontend/dist",
                         "cmd/praimate-gui/frontend/src/assets", "fake-bin"]:
                (fixture / name).mkdir(parents=True, exist_ok=True)
            for name in ["build.sh", "install.sh", "install.ps1"]:
                shutil.copy(ROOT / "scripts" / name, fixture / "scripts" / name)
            shutil.copytree(ROOT / "scripts/server",fixture / "scripts/server")
            for doc in (ROOT / "docs").glob("*.md"):
                (fixture / "docs" / doc.name).write_text("fixture")
            for name in ["README.md", "LICENSE", "docs/assets/monke-icon.png",
                         "examples/praimate_agent_capture.py", "examples/praimate_agent_review.py",
                         "cmd/praimate-gui/frontend/dist/index.html", "cmd/praimate-gui/frontend/src/assets/monke-icon.png"]:
                (fixture / name).write_text("fixture")
            fake_go = fixture / "fake-bin/go"
            fake_go.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args=sys.argv[1:]
if args[0]=='env':
    print('linux' if args[1]=='GOOS' else 'amd64')
else:
    with open(os.environ['PRAIMATE_TEST_BUILD_LOG'],'a') as log: log.write(json.dumps(args)+'\\n')
    out=pathlib.Path(args[args.index('-o')+1]);out.parent.mkdir(parents=True,exist_ok=True);out.write_text('fixture executable')
''')
            fake_go.chmod(0o755)
            log = fixture / "build-log.jsonl"
            env = dict(os.environ, PATH=str(fake_go.parent) + os.pathsep + os.environ["PATH"],
                       PRAIMATE_TEST_BUILD_LOG=str(log))
            for edition in ["server", "desktop"]:
                subprocess.run(["bash", "scripts/build.sh", "--edition=" + edition, "windows-amd64"],
                               cwd=fixture, env=env, check=True, capture_output=True, text=True)
                dist = fixture / ("dist/server" if edition == "server" else "dist")
                prefix = "praimate-server" if edition == "server" else "praimate"
                with zipfile.ZipFile(dist / (prefix + "-windows-amd64.zip")) as archive:
                    self.assertEqual(archive.read("windows-amd64/praimate-edition").decode().strip(), edition)
                calls = [json.loads(line) for line in log.read_text().splitlines()]
                self.assertEqual(len(calls), 4)
                for call in calls:
                    tags = call[call.index("-tags") + 1].split(",") if "-tags" in call else []
                    shared=Path(call[call.index("-o")+1]).name in ["wpc.exe","praimate-cli.exe"]
                    self.assertEqual("server" in tags, edition == "server" and not shared)
                log.unlink()
            self.assertTrue((fixture / "dist/server/praimate-server-windows-amd64.zip").exists())
            self.assertTrue((fixture / "dist/praimate-windows-amd64.zip").exists())
            subprocess.run(["bash","scripts/build.sh","--edition=server","--reuse-shared=dist","windows-amd64"],cwd=fixture,env=env,check=True,capture_output=True,text=True)
            self.assertEqual(len(log.read_text().splitlines()),2,"Shared binaries were rebuilt instead of reused")


if __name__ == "__main__":
    unittest.main()
