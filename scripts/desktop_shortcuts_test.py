"""Exercise launcher generation in temporary homes without installing anything."""
import os
from pathlib import Path
import plistlib
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SOURCE = (ROOT / "scripts/install.sh").read_text()
FUNCTIONS = SOURCE[SOURCE.index("# Desktop Entry strings"):SOURCE.index('\nICON_SRC=""')]


class DesktopShortcuts(unittest.TestCase):
    def generate(self, root, platform):
        dest = root / 'bin space $cash %tools "quote" `tick` \\slash'
        dest.mkdir()
        home = root / "home"
        (home / "Desktop").mkdir(parents=True)
        (dest / "praimate-gui").write_text('#!/bin/sh\nprintf launched > "$LAUNCH_RECORD"\n')
        (dest / "praimate-gui").chmod(0o755)
        env = {**os.environ, "HOME": str(home), "XDG_DATA_HOME": str(root / "data"),
               "DEST": str(dest), "LAUNCH_RECORD": str(root / "launched")}
        # This tests bundle/desktop generation, not native signing or iconutil.
        setup = 'c_grn() { :; }; xattr() { :; }; codesign() { :; }; uname() { printf "%s" "$TEST_OS"; };\n'
        subprocess.run(["bash", "-eu", "-c", setup + FUNCTIONS + '\ncreate_shortcuts "$1"',
                        "test", str(ROOT / "docs/assets/monke-icon.png")],
                       env={**env, "TEST_OS": platform}, check=True, capture_output=True)
        return home, dest, env

    def test_linux_exec_quoting_and_icon(self):
        try:
            from gi.repository import Gio
        except ImportError:
            self.skipTest("GObject introspection is needed to exercise the desktop parser")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            _, dest, env = self.generate(root, "Linux")
            entry = root / "data/applications/praimate.desktop"
            app = Gio.DesktopAppInfo.new_from_filename(str(entry))
            self.assertIsNotNone(app)
            # GIO applies both Desktop Entry string decoding and Exec quoting.
            context = Gio.AppLaunchContext()
            context.setenv("LAUNCH_RECORD", env["LAUNCH_RECORD"])
            app.launch([], context)
            # Reap through a bounded wait for the asynchronous desktop launch.
            import time
            for _ in range(100):
                if (root / "launched").exists():
                    break
                time.sleep(.01)
            self.assertEqual((root / "launched").read_text(), "launched")
            self.assertEqual(app.get_startup_wm_class(), "praimate")
            self.assertTrue((root / "data/icons/hicolor/512x512/apps/praimate.png").is_file())

    def test_macos_bundle_launcher_and_icon(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            home, dest, env = self.generate(root, "Darwin")
            app = home / "Applications/PrAImate.app"
            plist = plistlib.loads((app / "Contents/Info.plist").read_bytes())
            self.assertEqual(plist["CFBundleIdentifier"], "com.praimate.desktop")
            self.assertTrue((app / "Contents/Resources" / plist["CFBundleIconFile"]).is_file())
            self.assertEqual((home / "Desktop/PrAImate.app").resolve(), app)
            subprocess.run([str(app / "Contents/MacOS" / plist["CFBundleExecutable"])], env=env, check=True)
            self.assertEqual((root / "launched").read_text(), "launched")


if __name__ == "__main__":
    unittest.main()
