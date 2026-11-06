import hashlib
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import install_duckdb_runtime as runtime


class DuckDBRuntimeTests(unittest.TestCase):
    def test_verified_existing_library_uses_no_network(self):
        library = b"official library fixture"
        manifest = {"version": "fixture", "archives": {"linux-amd64": {
            "name": "fixture.zip", "library_sha256": hashlib.sha256(library).hexdigest()}}}
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / "libduckdb.so").write_bytes(library)
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen") as network:
                self.assertEqual(runtime.install("linux-amd64", output), output / "libduckdb.so")
            network.assert_not_called()
            self.assertTrue((output / "DuckDB-LICENSE.txt").is_file())

    def test_corrupt_download_preserves_existing_file(self):
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr("libduckdb.so", b"corrupt library")
        data = stream.getvalue()
        manifest = {"version": "fixture", "archives": {"linux-amd64": {
            "name": "fixture.zip", "sha256": hashlib.sha256(data).hexdigest(),
            "library_sha256": hashlib.sha256(b"expected library").hexdigest()}}}
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            (output / "libduckdb.so").write_bytes(b"keep existing file")
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(data)):
                with self.assertRaisesRegex(RuntimeError, "library integrity"):
                    runtime.install("linux-amd64", output)
            self.assertEqual((output / "libduckdb.so").read_bytes(), b"keep existing file")

    def test_manifest_matches_runtime_loader_digests(self):
        source = (runtime.ROOT / "internal/services/duckdb/native/api.go").read_text()
        for entry in runtime.MANIFEST["archives"].values():
            self.assertIn(entry["library_sha256"], source)
