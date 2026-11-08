import hashlib
import http.client
import io
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import MagicMock, patch
import urllib.error
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import install_duckdb_runtime as runtime


class DuckDBRuntimeTests(unittest.TestCase):
    def verified_archive(self):
        library = b"official library fixture"
        stream = io.BytesIO()
        with zipfile.ZipFile(stream, "w") as archive:
            archive.writestr("libduckdb.so", library)
        data = stream.getvalue()
        manifest = {"version": "fixture", "archives": {"linux-amd64": {
            "name": "fixture.zip", "sha256": hashlib.sha256(data).hexdigest(),
            "library_sha256": hashlib.sha256(library).hexdigest()}}}
        return library, data, manifest

    def test_transient_download_errors_retry_and_install_verified_library(self):
        library, data, manifest = self.verified_archive()
        errors = [urllib.error.HTTPError("https://example.test", code, "transient", {}, None)
                  for code in (408, 429, 500, 502, 503, 504)]
        errors += [urllib.error.URLError("connection reset"), TimeoutError("timed out")]
        for error in errors:
            with self.subTest(error=error), tempfile.TemporaryDirectory() as directory:
                output = Path(directory)
                with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", side_effect=[error, io.BytesIO(data)]) as network, patch.object(runtime.time, "sleep") as sleep:
                    path = runtime.install("linux-amd64", output)
                self.assertEqual(path.read_bytes(), library)
                self.assertTrue((output / "duckdb-runtime.json").is_file())
                self.assertTrue((output / "DuckDB-LICENSE.txt").is_file())
                self.assertEqual(network.call_count, 2)
                sleep.assert_called_once_with(1)

    def test_interrupted_response_restarts_download(self):
        library, data, manifest = self.verified_archive()
        response = MagicMock()
        response.__enter__.return_value = response
        response.read.side_effect = http.client.IncompleteRead(b"partial")
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", side_effect=[response, io.BytesIO(data)]) as network, patch.object(runtime.time, "sleep"):
                path = runtime.install("linux-amd64", directory)
            self.assertEqual(path.read_bytes(), library)
            self.assertEqual(network.call_count, 2)
            response.__exit__.assert_called_once()

    def test_persistent_gateway_error_has_bounded_retries_and_preserves_file(self):
        _, _, manifest = self.verified_archive()
        errors = [urllib.error.HTTPError("https://example.test", 504, "timeout", {}, None)
                  for _ in range(4)]
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            path = output / "libduckdb.so"
            path.write_bytes(b"keep existing file")
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", side_effect=errors) as network, patch.object(runtime.time, "sleep") as sleep:
                with self.assertRaises(urllib.error.HTTPError):
                    runtime.install("linux-amd64", output)
            self.assertEqual(network.call_count, 4)
            self.assertEqual([call.args[0] for call in sleep.call_args_list], [1, 2, 4])
            self.assertEqual(path.read_bytes(), b"keep existing file")
            self.assertEqual(list(output.iterdir()), [path])

    def test_permanent_http_error_does_not_retry(self):
        _, _, manifest = self.verified_archive()
        error = urllib.error.HTTPError("https://example.test", 404, "not found", {}, None)
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", side_effect=error) as network, patch.object(runtime.time, "sleep") as sleep:
                with self.assertRaises(urllib.error.HTTPError):
                    runtime.install("linux-amd64", directory)
            network.assert_called_once()
            sleep.assert_not_called()
            self.assertEqual(list(Path(directory).iterdir()), [])

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
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(data)) as network, patch.object(runtime.time, "sleep") as sleep:
                with self.assertRaisesRegex(RuntimeError, "library integrity"):
                    runtime.install("linux-amd64", output)
            self.assertEqual((output / "libduckdb.so").read_bytes(), b"keep existing file")
            network.assert_called_once()
            sleep.assert_not_called()

    def test_corrupt_archive_does_not_retry_or_publish(self):
        _, _, manifest = self.verified_archive()
        with tempfile.TemporaryDirectory() as directory:
            with patch.object(runtime, "MANIFEST", manifest), patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(b"corrupt archive")) as network, patch.object(runtime.time, "sleep") as sleep:
                with self.assertRaisesRegex(RuntimeError, "archive integrity"):
                    runtime.install("linux-amd64", directory)
            network.assert_called_once()
            sleep.assert_not_called()
            self.assertEqual(list(Path(directory).iterdir()), [])

    def test_manifest_matches_runtime_loader_digests(self):
        source = (runtime.ROOT / "internal/services/duckdb/native/api.go").read_text()
        for entry in runtime.MANIFEST["archives"].values():
            self.assertIn(entry["library_sha256"], source)
