import importlib.util
from pathlib import Path
import unittest
import json
import tempfile
from PIL import Image


SPEC = importlib.util.spec_from_file_location(
    "trino_agent_fixture", Path(__file__).resolve().parents[1] / "tools/verify_agent_trino_fixture.py"
)
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class TrinoAgentAcceptanceEvidenceTests(unittest.TestCase):
    def test_requires_every_original_native_acceptance(self):
        lines = ["--- PASS: " + name + " (1.00s)" for name in FIXTURE.REQUIRED]
        FIXTURE.verify_acceptance("\n".join(lines))
        for missing in range(len(lines)):
            with self.subTest(missing=missing), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance("\n".join(line for i, line in enumerate(lines) if i != missing))

    def test_tls_requires_both_original_authentication_failures(self):
        lines = ["--- PASS: " + name + " (1.00s)" for name in FIXTURE.REQUIRED]
        cases = ["    --- PASS: TestTrinoAgentNativeAuthenticationAndTrust/" + case + " (1.00s)" for case in ["wrong-password", "untrusted-certificate"]]
        FIXTURE.verify_acceptance("\n".join(lines + cases), tls=True)
        for missing in range(len(cases)):
            with self.subTest(missing=missing), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance("\n".join(lines + [line for i, line in enumerate(cases) if i != missing]), tls=True)

    def test_skip_generic_pass_and_similar_names_are_insufficient(self):
        for prefix, suffix in [("--- SKIP: ", ""), ("--- PASS: ", "Fake"), ("--- PASS: ", "/subtest")]:
            with self.subTest(prefix=prefix, suffix=suffix), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance("\n".join(prefix + name + suffix + " (1.00s)" for name in FIXTURE.REQUIRED) + "\nPASS")


class TrinoWorkbenchEvidenceScopeTests(unittest.TestCase):
    def evidence(self, base):
        for log, names in [('acceptance', FIXTURE.REQUIRED), ('provider-acceptance', FIXTURE.PROVIDER_REQUIRED)]:
            (base / (log + '.log')).write_text('\n'.join('--- PASS: ' + name + ' (1s)' for name in names))
        for log, name in FIXTURE.UI_REQUIRED.items():
            (base / (log + '-acceptance.log')).write_text('--- PASS: ' + name + ' (1s)')
        editor = base / 'editor-screenshots'
        shell = base / 'shell-screenshots'
        editor.mkdir()
        shell.mkdir()
        controls = {}
        for language in ['zh-Hans', 'en']:
            for size, width in [('wide', 920), ('narrow', 720)]:
                for tab, labels in FIXTURE.EDITOR_CONTROLS.items():
                    name = language + '-' + size + '-' + tab + '.png'
                    Image.new('RGB', (width, 780)).save(editor / name)
                    controls[name] = {label: {'X': 10, 'Y': 20, 'W': 100, 'H': 30} for label in labels}
        (editor / 'controls.json').write_text(json.dumps(controls))
        for name in FIXTURE.SHELL_IMAGES:
            Image.new('RGB', (1280, 800)).save(shell / ('trino-managed-' + name + '.png'))
        record = {'object': {'ProfileID': 1, 'Catalog': 'memory', 'Schema': 'default'}, 'columns': [{'Name': name} for name in ['id', 'note', 'payload', 'items']], 'values': [{'kind': 'integer', 'text': '9007199254740993'}, {'kind': 'text', 'text': 'native shared grid'}, {'kind': 'binary', 'bytes': 'AP8='}, {'kind': 'json', 'text': '[9007199254740993]'}]}
        (shell / 'complete-export.jsonl').write_text(json.dumps(record) + '\n')

    def test_provider_only_and_bridge_cannot_claim_public_workbench(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            self.evidence(base)
            for scope in ['bridge', 'provider']:
                self.assertFalse(FIXTURE.verify_scope_evidence(base, scope)['public_managed_provider'])
            result = FIXTURE.verify_scope_evidence(base, 'workbench')
            self.assertTrue(result['public_managed_provider'])
            self.assertEqual(len(result['screenshots']), 17)

    def test_every_native_provider_and_public_ui_pass_is_required(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            self.evidence(base)
            files = ['provider-acceptance.log', 'editor-acceptance.log', 'shell-acceptance.log']
            for name in files:
                file = base / name
                original = file.read_text()
                file.write_text('--- SKIP: required test (0s)\nPASS')
                with self.subTest(name=name), self.assertRaises(RuntimeError):
                    FIXTURE.verify_scope_evidence(base, 'workbench')
                file.write_text(original)

    def test_complete_export_requires_original_binary_composite_and_qualified_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            self.evidence(base)
            path = base / 'shell-screenshots/complete-export.jsonl'
            original = path.read_text()
            record = json.loads(original)
            for field, value in [('kind', 'text'), ('bytes', 'AP4='), ('truncated', True)]:
                modified = json.loads(original)
                modified['values'][2][field] = value
                path.write_text(json.dumps(modified))
                with self.subTest(field=field), self.assertRaises(RuntimeError):
                    FIXTURE.verify_complete_grid_export(base)
            record['object']['Schema'] = 'other'
            path.write_text(json.dumps(record))
            with self.assertRaises(RuntimeError):
                FIXTURE.verify_complete_grid_export(base)
            path.unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_complete_grid_export(base)

    def test_provider_tls_requires_actual_password_trust_and_hostname_failures(self):
        lines = ['--- PASS: ' + name + ' (1s)' for name in FIXTURE.PROVIDER_REQUIRED]
        cases = ['--- PASS: TestTrinoManagedNativeAuthenticationAndExplicitRecovery/' + name + ' (1s)' for name in ['wrong-password', 'untrusted-certificate', 'wrong-hostname']]
        FIXTURE.verify_provider_acceptance('\n'.join(lines + cases), tls=True)
        for missing in cases:
            with self.subTest(missing=missing), self.assertRaises(RuntimeError):
                FIXTURE.verify_provider_acceptance('\n'.join(lines + [line for line in cases if line != missing]), tls=True)

    def test_insert_scope_requires_real_native_insert_passes_and_original_receipt(self):
        lines = ['--- PASS: ' + name + ' (1s)' for name in FIXTURE.PROVIDER_REQUIRED]
        inserts = ['--- PASS: ' + name + ' (1s)' for name in FIXTURE.INSERT_REQUIRED]
        FIXTURE.verify_provider_acceptance('\n'.join(lines + inserts), writes=True)
        for missing in inserts:
            with self.subTest(missing=missing), self.assertRaises(RuntimeError):
                FIXTURE.verify_provider_acceptance('\n'.join(lines + [line for line in inserts if line != missing]), writes=True)
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            self.evidence(base)
            for name in ['insert-confirmation', 'insert-receipt']:
                Image.new('RGB', (1280, 800)).save(base / ('shell-screenshots/trino-managed-' + name + '.png'))
            (base / 'http-shell-screenshots').mkdir()
            for name in ['insert-confirmation', 'insert-receipt']:
                Image.new('RGB', (1280, 800)).save(base / ('http-shell-screenshots/trino-live-' + name + '.png'))
            path = base / 'shell-screenshots/insert-receipt.json'
            native = json.dumps({'ExecutorAcknowledged': True, 'Result': {'affected_rows': 1}})
            receipt = {'Receipt': {'Object': {'ProfileID': 1}, 'Statements': [{'State': 'acknowledged', 'AffectedRows': 1, 'NativeJSON': native}]}}
            path.write_text(json.dumps(receipt))
            self.assertEqual(len(FIXTURE.verify_workbench_images(base, writes=True)), 21)
            for state in ['unknown', 'failed', 'not_sent']:
                receipt['Receipt']['Statements'][0]['State'] = state
                path.write_text(json.dumps(receipt))
                with self.subTest(state=state), self.assertRaises(RuntimeError):
                    FIXTURE.verify_workbench_images(base, writes=True)

    def test_missing_clipped_and_corrupt_screenshots_fail(self):
        with tempfile.TemporaryDirectory() as temp:
            base = Path(temp)
            self.evidence(base)
            for image in list((base / 'editor-screenshots').glob('*.png')) + list((base / 'shell-screenshots').glob('*.png')):
                original = image.read_bytes()
                image.unlink()
                with self.subTest(missing=image.name), self.assertRaises(FileNotFoundError):
                    FIXTURE.verify_workbench_images(base)
                image.write_bytes(original)
            manifest = base / 'editor-screenshots/controls.json'
            original = manifest.read_text()
            data = json.loads(original)
            image_name = next(iter(data))
            label = next(iter(data[image_name]))
            for replacement in [{}, {'X': 900, 'Y': 0, 'W': 100, 'H': 30}, {'X': 0, 'Y': float('nan'), 'W': 100, 'H': 30}]:
                data[image_name][label] = replacement
                manifest.write_text(json.dumps(data))
                with self.subTest(box=replacement), self.assertRaises(RuntimeError):
                    FIXTURE.verify_workbench_images(base)
            manifest.write_text(original)
            image = base / 'shell-screenshots/trino-managed-grid.png'
            Image.new('RGB', (1280, 10)).save(image)
            with self.assertRaises(RuntimeError):
                FIXTURE.verify_workbench_images(base)
            image.write_bytes(b'not an image')
            with self.assertRaises(OSError):
                FIXTURE.verify_workbench_images(base)


if __name__ == "__main__":
    unittest.main()
