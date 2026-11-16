import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import zipfile

SPEC = importlib.util.spec_from_file_location('databend_builder',
    Path(__file__).resolve().parents[1] / 'tools/build_databend_package.py')
BUILDER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILDER)


class DatabendPackageTests(unittest.TestCase):
    def test_offline_fixed_artifacts_reject_changes_and_escaped_paths(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            (cache / 'original.jar').write_bytes(b'original')
            item = {'path': 'original.jar', 'sha256': BUILDER.raw_sha(b'original'),
                    'url': 'https://repo.maven.apache.org/maven2/original.jar'}
            self.assertEqual(BUILDER.fetch(item, cache, True), cache / 'original.jar')
            (cache / 'original.jar').write_bytes(b'changed')
            with self.assertRaises(RuntimeError):
                BUILDER.fetch(item, cache, True)
            for path in ['../foreign', '/foreign', 'a/../b', 'a//b', 'a\\b', 'x:y']:
                with self.subTest(path=path), self.assertRaises(RuntimeError):
                    BUILDER.fetch(dict(item, path=path), cache, True)

    def test_archive_is_reproducible_and_preserves_executable_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / 'package'
            package.mkdir()
            (package / 'java').write_bytes(b'original runtime')
            (package / 'java').chmod(0o755)
            (package / 'LICENSE').write_bytes(b'original license')
            BUILDER.canonical_archive(package, root / 'a.zip')
            (package / 'java').touch()
            BUILDER.canonical_archive(package, root / 'b.zip')
            self.assertEqual(BUILDER.sha(root / 'a.zip'), BUILDER.sha(root / 'b.zip'))
            with zipfile.ZipFile(root / 'a.zip') as z:
                self.assertEqual(z.getinfo('java').external_attr >> 16, 0o100755)
                self.assertEqual(z.getinfo('LICENSE').external_attr >> 16, 0o100644)

    def test_complete_payload_rejects_missing_changed_and_undeclared_artifacts(self):
        for change in ['none', 'missing', 'changed', 'extra', 'escape']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / 'java').write_bytes(b'original')
                manifest = {'key': 'databend', 'runtime': 'java', 'main_class': BUILDER.MAIN,
                            'files': [{'path': 'java', 'sha256': BUILDER.raw_sha(b'original')}]}
                if change == 'missing':
                    (root / 'java').unlink()
                elif change == 'changed':
                    (root / 'java').write_bytes(b'changed')
                elif change == 'extra':
                    (root / 'foreign').write_bytes(b'foreign')
                elif change == 'escape':
                    manifest['files'][0]['path'] = '../foreign'
                (root / 'manifest.json').write_text(json.dumps(manifest))
                if change == 'none':
                    self.assertEqual(BUILDER.verify_package(root), manifest)
                else:
                    with self.assertRaises((RuntimeError, FileNotFoundError)):
                        BUILDER.verify_package(root)

    def test_source_inventory_rejects_changed_revision_and_added_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'agents/common/src/main/java/Original.java'
            source.parent.mkdir(parents=True)
            source.write_bytes(b'original')
            pins = {'dbx_revision': 'original', 'dbx_source_files': [
                {'path': source.relative_to(root).as_posix(), 'sha256': BUILDER.sha(source)}]}
            with mock.patch.object(BUILDER.subprocess, 'check_output', return_value='original\n'):
                BUILDER.verify_sources(root, pins)
                source.with_name('Foreign.java').write_bytes(b'foreign')
                with self.assertRaises(RuntimeError):
                    BUILDER.verify_sources(root, pins)
            with mock.patch.object(BUILDER.subprocess, 'check_output', return_value='replacement\n'):
                with self.assertRaises(RuntimeError):
                    BUILDER.verify_sources(root, pins)

    def test_shaded_mapping_requires_exact_unique_ownership_or_original_source(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with zipfile.ZipFile(root / 'upstream.jar', 'w') as z:
                z.writestr('third/Native.class', b'original bytes')
            with zipfile.ZipFile(root / 'sources.jar', 'w') as z:
                z.writestr('com/databend/jdbc/Container.java',
                           'package com.databend.jdbc; class Secondary {}')
            with zipfile.ZipFile(root / 'jdbc.jar', 'w') as z:
                z.writestr('third/Native.class', b'original bytes')
                z.writestr('com/databend/jdbc/Secondary.class', b'compiled original')
            result = BUILDER.shaded_provenance(root / 'jdbc.jar', {'upstream': root / 'upstream.jar'},
                                               root / 'sources.jar', [])
            self.assertEqual(len(result['classes']), 2)
            with zipfile.ZipFile(root / 'jdbc.jar', 'a') as z:
                z.writestr('unknown/Foreign.class', b'foreign')
            with self.assertRaises(RuntimeError):
                BUILDER.shaded_provenance(root / 'jdbc.jar', {'upstream': root / 'upstream.jar'},
                                           root / 'sources.jar', [])


if __name__ == '__main__':
    unittest.main()
