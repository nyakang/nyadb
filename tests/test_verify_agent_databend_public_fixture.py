import copy
import json
from pathlib import Path
import sys
import tempfile
import unittest

from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / 'tools'))
import verify_agent_databend_public_fixture as public


class DatabendPublicEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.base = Path(self.directory.name)
        self.editor = {
            'public_provider_registered': True, 'test_preserved_session': True,
            'driver_version': 'jdbc-0.4.8-agent-0.1.82',
            'driver_sha256': '7069b794a21d8995b67d6918c6c106967ce94a18ae9a470159401bce18032bfd',
        }
        result = {'columns': [{'name': 'number of rows updated'}], 'rows': [[1]]}
        native = {'Receipt': {'ExecutorAcknowledged': True, 'Dispatched': True, 'Result': result},
                  'Original': {'jsonrpc': '2.0', 'id': 11, 'result': result}}
        self.shell = {
            'public_provider_registered': True, 'typed_grid': True,
            'preview_cancel_kept_draft': True, 'sql_text_retained': True,
            'recovery_replayed': False, 'original_generation': 1,
            'replacement_generation': 2, 'processes': 2, 'vault_recovery': True, 'menu_connect': True,
            'receipt': {'CommitConfirmed': False, 'Receipt': {'Statements': [
                {'State': 'acknowledged', 'AffectedRows': 1, 'NativeJSON': json.dumps(native)}]}},
        }
        self.controls = {}
        for language in ['zh-Hans', 'en']:
            for w, h in [(920, 780), (720, 520)]:
                self.image(f'{language}-{w}x{h}-public-chooser.png', w, h)
                for tab, count in [('general', 5), ('advanced', 7), ('ssl', 6)]:
                    name = f'{language}-{w}x{h}-public-editor-{tab}'
                    self.image(name + '.png', w, h)
                    self.controls[name] = {str(i): {'Screenshot': name + '.png',
                        'X': 20, 'Y': 30+i*35, 'W': 100, 'H': 28, 'ExpectedHeight': 28} for i in range(count)}
            for w, h in [(1280, 800), (720, 520)]:
                for phase in ['grid', 'preview', 'receipt', 'disconnected', 'recovered']:
                    self.image(f'workbench/{public.REQUIRED[1][1]}/{language}/public-{phase}-{w}x{h}.png', w, h)

    def image(self, name, w, h):
        path = self.base / name
        path.parent.mkdir(parents=True, exist_ok=True)
        Image.new('RGB', (w, h), 'white').save(path)

    def verify(self):
        for name, value in [('public-bootstrap.jsonl', self.editor), ('public-shell.jsonl', self.shell),
                            ('public-editor-controls.json', self.controls)]:
            (self.base / name).write_text(json.dumps(value) + '\n')
        return public.verify_public(self.base)

    def test_complete_scoped_evidence_passes(self):
        self.assertEqual(len(self.verify()['image_sha256']), 36)

    def test_rejects_false_public_or_replayed_recovery(self):
        original = copy.deepcopy(self.shell)
        for key, value in [('public_provider_registered', False), ('recovery_replayed', True),
                           ('replacement_generation', 1), ('processes', 1)]:
            with self.subTest(key=key):
                self.shell = copy.deepcopy(original)
                self.shell[key] = value
                with self.assertRaises(RuntimeError):
                    self.verify()

    def test_rejects_fabricated_original_acknowledgement(self):
        statement = self.shell['receipt']['Receipt']['Statements'][0]
        native = json.loads(statement['NativeJSON'])
        native['Original']['result']['rows'] = [[2]]
        statement['NativeJSON'] = json.dumps(native)
        with self.assertRaisesRegex(RuntimeError, 'executor response'):
            self.verify()

    def test_rejects_clipped_control_and_missing_capture(self):
        box = next(iter(next(iter(self.controls.values())).values()))
        box['Y'] = 1000
        with self.assertRaisesRegex(RuntimeError, 'clipped'):
            self.verify()
        box['Y'] = 30
        (self.base / 'en-720x520-public-chooser.png').unlink()
        with self.assertRaisesRegex(RuntimeError, 'captures missing'):
            self.verify()


if __name__ == '__main__':
    unittest.main()
