import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import zipfile

SPEC = importlib.util.spec_from_file_location('prestosql_builder',
    Path(__file__).resolve().parents[1] / 'tools/build_prestosql_package.py')
BUILDER = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILDER)


class PrestoSQLPackageTests(unittest.TestCase):
    def test_offline_inputs_reject_mutation_missing_and_escaped_cache(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cache = root / 'cache'
            cache.mkdir()
            item = {'path': 'original.jar', 'sha256': BUILDER.raw_sha(b'original'),
                    'url': 'https://repo.maven.apache.org/maven2/original.jar'}
            with self.assertRaises(RuntimeError):
                BUILDER.fetch(item, cache, True)
            (cache / item['path']).write_bytes(b'original')
            self.assertEqual(BUILDER.fetch(item, cache, True), cache / item['path'])
            (cache / item['path']).write_bytes(b'changed')
            with self.assertRaises(RuntimeError):
                BUILDER.fetch(item, cache, True)
            (root / 'foreign.jar').write_bytes(b'original')
            (cache / 'linked.jar').symlink_to(root / 'foreign.jar')
            for name in ['../foreign.jar', '/foreign.jar', 'a/../b', 'a//b', 'a\\b', 'a:b', 'linked.jar']:
                with self.subTest(name=name), self.assertRaises(RuntimeError):
                    BUILDER.fetch(dict(item, path=name), cache, True)

    def test_archive_reproducibility_preserves_execution_mode(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / 'package'
            package.mkdir()
            (package / 'java').write_bytes(b'runtime')
            (package / 'java').chmod(0o755)
            (package / 'LICENSE').write_bytes(b'license')
            BUILDER.canonical_archive(package, root / 'first.zip')
            (package / 'java').touch()
            BUILDER.canonical_archive(package, root / 'second.zip')
            self.assertEqual(BUILDER.sha(root / 'first.zip'), BUILDER.sha(root / 'second.zip'))
            with zipfile.ZipFile(root / 'first.zip') as archive:
                self.assertEqual(archive.getinfo('java').external_attr >> 16, 0o100755)

    def test_payload_verification_rejects_case_aliases_and_execution_changes(self):
        for change in ['none', 'case_alias', 'mode', 'bytes', 'missing', 'reference', 'driver', 'symlink']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                names = ['java', 'sources.json', 'LICENSE', 'dbx-jdbc-plugin.jar', 'jdbc/presto-jdbc-350.jar']
                for name in names:
                    path = root / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(b'original')
                    path.chmod(0o755 if name == 'java' else 0o644)
                manifest = {'key': 'prestosql', 'runtime': 'java', 'main_class': BUILDER.MAIN,
                            'executable': 'java', 'provenance': 'sources.json', 'licenses': ['LICENSE'],
                            'classpath': ['dbx-jdbc-plugin.jar'],
                            'jdbc': {'class': 'io.prestosql.jdbc.PrestoDriver', 'classpath': ['jdbc/presto-jdbc-350.jar']},
                            'files': [{'path': name, 'sha256': BUILDER.sha(root / name), 'executable': name == 'java'}
                                      for name in names]}
                if change == 'case_alias':
                    (root / 'JAVA').write_bytes(b'foreign')
                elif change == 'mode':
                    (root / 'java').chmod(0o644)
                elif change == 'bytes':
                    (root / 'java').write_bytes(b'foreign')
                elif change == 'missing':
                    (root / 'java').unlink()
                elif change == 'reference':
                    manifest['licenses'].append('foreign')
                elif change == 'driver':
                    manifest['jdbc']['class'] = 'foreign.Driver'
                elif change == 'symlink':
                    (root / 'LICENSE').unlink()
                    (root / 'LICENSE').symlink_to(root / 'sources.json')
                (root / 'manifest.json').write_text(json.dumps(manifest))
                if change == 'none':
                    self.assertEqual(BUILDER.verify_package(root), manifest)
                else:
                    with self.assertRaises((RuntimeError, FileNotFoundError)):
                        BUILDER.verify_package(root)

    def test_original_source_inventory_rejects_unreviewed_changes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = root / 'plugins/jdbc/src/main/java/Original.java'
            source.parent.mkdir(parents=True)
            source.write_bytes(b'original')
            pins = {'dbx_revision': 'original', 'dbx_source_files': [
                {'path': source.relative_to(root).as_posix(), 'sha256': BUILDER.sha(source)}]}
            with mock.patch.object(BUILDER.subprocess, 'check_output', return_value='original\n'):
                BUILDER.verify_sources(root, pins)
                source.write_bytes(b'changed')
                with self.assertRaises(RuntimeError):
                    BUILDER.verify_sources(root, pins)
                source.write_bytes(b'original')
                source.with_name('Foreign.java').write_bytes(b'foreign')
                with self.assertRaises(RuntimeError):
                    BUILDER.verify_sources(root, pins)
            with mock.patch.object(BUILDER.subprocess, 'check_output', return_value='replacement\n'):
                with self.assertRaises(RuntimeError):
                    BUILDER.verify_sources(root, pins)

    def test_plugin_classes_require_exact_unique_original_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            classes = root / 'classes'
            classes.mkdir()
            (classes / 'Original.class').write_bytes(b'original')
            expected = {'Original.class': BUILDER.raw_sha(b'original')}
            with zipfile.ZipFile(root / 'dependency.jar', 'w') as archive:
                archive.writestr('Dependency.class', b'dependency')
            with zipfile.ZipFile(root / 'plugin.jar', 'w') as archive:
                archive.writestr('Original.class', b'original')
                archive.writestr('Dependency.class', b'dependency')
            args = (root / 'plugin.jar', {'dependency': root / 'dependency.jar'}, classes, expected)
            self.assertEqual(len(BUILDER.plugin_provenance(*args)), 2)
            (classes / 'Original.class').write_bytes(b'changed')
            with self.assertRaises(RuntimeError):
                BUILDER.plugin_provenance(*args)
            (classes / 'Original.class').write_bytes(b'original')
            with zipfile.ZipFile(root / 'plugin.jar', 'w') as archive:
                archive.writestr('Original.class', b'original')
                archive.writestr('Dependency.class', b'changed')
            with self.assertRaises(RuntimeError):
                BUILDER.plugin_provenance(*args)

    def test_jdbc_mapping_distinguishes_relocation_from_exact_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with zipfile.ZipFile(root / 'dependency.jar', 'w') as archive:
                archive.writestr('com/google/thirdparty/Native.class', b'original')
            with zipfile.ZipFile(root / 'sources.jar', 'w') as archive:
                archive.writestr('com/facebook/presto/jdbc/PrestoDriver.java', b'original source')
            with zipfile.ZipFile(root / 'jdbc.jar', 'w') as archive:
                archive.writestr('io/prestosql/jdbc/$internal/guava/Native.class', b'relocated')
                archive.writestr('com/facebook/presto/jdbc/PrestoDriver.class', b'compiled')
            args = (root / 'jdbc.jar', {'dependency': root / 'dependency.jar'}, root / 'sources.jar',
                    [('io/prestosql/jdbc/$internal/guava/', 'com/google/common/')])
            rows = BUILDER.jdbc_provenance(*args)['classes']
            self.assertEqual(len(rows), 2)
            self.assertFalse(rows[1]['exact_bytes'])
            self.assertEqual(rows[0]['coordinate'], BUILDER.DRIVER)
            with zipfile.ZipFile(root / 'jdbc.jar', 'a') as archive:
                archive.writestr('unknown/Foreign.class', b'foreign')
            with self.assertRaises(RuntimeError):
                BUILDER.jdbc_provenance(*args)


if __name__ == '__main__':
    unittest.main()
