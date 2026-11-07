import importlib.util
from pathlib import Path
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('stage',Path(__file__).with_name('stage-release-assets.py'));module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
class ReleaseAssets(unittest.TestCase):
    def test_combined_release_has_distinct_archives_and_one_shared_copy(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);(root/'dist/server').mkdir(parents=True)
            for path in ['dist/praimate-linux-amd64.tar.gz','dist/server/praimate-server-linux-amd64.tar.gz','dist/praimate-cli-linux-amd64','dist/server/praimate-cli-linux-amd64','dist/praimate-code-linux-amd64']:(root/path).write_bytes(b'fixture')
            output=module.stage(root,'all','linux-amd64')
            self.assertEqual({p.name for p in output.iterdir()},{'praimate-linux-amd64.tar.gz','praimate-server-linux-amd64.tar.gz','praimate-cli-linux-amd64','praimate-code-linux-amd64'})
            (root/'dist/server/praimate-cli-linux-amd64').write_bytes(b'different edition')
            with self.assertRaises(ValueError):module.stage(root,'all','linux-amd64')
if __name__=='__main__':unittest.main()
