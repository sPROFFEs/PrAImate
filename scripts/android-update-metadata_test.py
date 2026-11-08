#!/usr/bin/env python3
import importlib.util
from pathlib import Path
import tempfile
import unittest
import sys

sys.dont_write_bytecode = True

spec = importlib.util.spec_from_file_location("android_update", Path(__file__).with_name("android-update-metadata.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class AndroidUpdateMetadataTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.apk = self.root / module.APK
        self.apk.write_bytes(b"synthetic APK for checksum staging")
        self.release = {"tagName": "v1.3.3", "isDraft": False, "body": "Host release notes.\n"}
        self.hosts = "a" * 64 + "  praimate-linux-amd64.tar.gz\n" + "b" * 64 + "  praimate-server-windows-amd64.zip\n"
        self.manifest = self.hosts + "c" * 64 + "  " + module.APK + "\n"

    def stage(self, version="1.3.4"):
        module.stage(self.release, self.manifest, self.apk, version, "d" * 40, self.root / "output")

    def test_retains_host_checksums_and_notes_when_replacing_the_apk(self):
        self.stage()
        sums = (self.root / "output/SHA256SUMS").read_text()
        self.assertTrue(sums.startswith(self.hosts))
        self.assertEqual(sums.count(module.APK), 1)
        self.assertIn(module.hashlib.sha256(self.apk.read_bytes()).hexdigest(), sums)
        notes = (self.root / "output/notes.md").read_text()
        self.assertTrue(notes.startswith(self.release["body"]))
        self.assertEqual(notes.count("<!-- praimate-android-version:1.3.4 -->"), 1)

    def test_followup_android_update_replaces_its_own_metadata_only(self):
        self.stage()
        self.release["body"] = (self.root / "output/notes.md").read_text()
        self.manifest = (self.root / "output/SHA256SUMS").read_text()
        self.stage("1.3.5")
        notes = (self.root / "output/notes.md").read_text()
        self.assertEqual(notes.count("<!-- praimate-android-update:start -->"), 1)
        self.assertNotIn("Android client 1.3.4", notes)
        self.assertTrue((self.root / "output/SHA256SUMS").read_text().startswith(self.hosts))

    def test_refuses_downgrades_duplicates_and_incomplete_notes(self):
        for invalid in ("1.3.3", "1.3.2", "not-a-version", "1.3.4-debug"):
            with self.subTest(version=invalid), self.assertRaises(ValueError):
                self.stage(invalid)
        self.manifest += "e" * 64 + "  " + module.APK + "\n"
        with self.assertRaises(ValueError):
            self.stage()
        self.manifest = self.hosts + "c" * 64 + "  " + module.APK + "\n"
        self.release["body"] += "<!-- praimate-android-update:start -->"
        with self.assertRaises(ValueError):
            self.stage()


if __name__ == "__main__":
    unittest.main()
