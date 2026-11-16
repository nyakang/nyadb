import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location('zookeeper_fixture', Path(__file__).resolve().parents[1] / 'tools/verify_agent_zookeeper_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class ZooKeeperAcceptanceEvidenceTests(unittest.TestCase):
    def public_image_fixture(self, base):
        from PIL import Image
        controls = {}
        paths = {}
        for language in ['zh-Hans', 'en']:
            for width, height in [(920, 780), (720, 520)]:
                for tab, labels in FIXTURE.EDITOR_CONTROLS.items():
                    name = f'{language}-{width}x{height}-public-editor-{tab}'
                    controls[name] = {label: {'X': 50, 'Y': 100, 'W': 120, 'H': 24,
                        'ExpectedHeight': 24, 'Screenshot': name + '.png'} for label in labels}
                    paths[name + '.png'] = (width, height)
                paths[f'{language}-{width}x{height}-public-chooser.png'] = (width, height)
            for width, height in [(1280, 800), (720, 520)]:
                for stage in ['confirmation', 'receipt', 'recovery']:
                    paths[f'{language}-{width}x{height}-public-shell-{stage}.png'] = (width, height)
                paths[f'{language}-{width}x{height}-public-unknown-write.png'] = (width, height)
        (base / 'public-editor-controls.json').write_text(json.dumps(controls))
        # Pixel validity alone does not prove behavior; the required real Go
        # tests remain an independent prerequisite for public acceptance.
        strip = Image.new('RGB', (32, 1))
        strip.putdata([(i * 7, i * 3, i * 5) for i in range(32)])
        for name, dimensions in paths.items():
            strip.resize(dimensions, Image.Resampling.NEAREST).save(base / name)
        return controls, paths

    def test_public_images_require_both_locales_and_all_stages(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            _, paths = self.public_image_fixture(base)
            self.assertEqual(len(FIXTURE.verify_public_images(base)), len(paths))
            (base / 'en-720x520-public-shell-recovery.png').unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_public_images(base)

    def test_public_images_reject_clipped_controls_and_blank_pixels(self):
        from PIL import Image
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            controls, _ = self.public_image_fixture(base)
            box = controls['en-720x520-public-editor-general']['导入驱动包']
            box['H'] = 1
            (base / 'public-editor-controls.json').write_text(json.dumps(controls))
            with self.assertRaisesRegex(RuntimeError, 'control clipped'):
                FIXTURE.verify_public_images(base)
            box['H'] = 24
            (base / 'public-editor-controls.json').write_text(json.dumps(controls))
            Image.new('RGB', (720, 520), 'black').save(base / 'en-720x520-public-shell-recovery.png')
            with self.assertRaisesRegex(RuntimeError, 'rendered UI pixels'):
                FIXTURE.verify_public_images(base)

    def test_foundation_modes_never_require_or_accept_full_public_workflow(self):
        for flags in [{}, {'provider': True}, {'public_runtime': True}]:
            foundation = FIXTURE.acceptance_requirements(**flags)
            self.assertFalse(set(foundation) & set(FIXTURE.PUBLIC_REQUIRED))
            output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in foundation)
            with self.subTest(flags=flags), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output, FIXTURE.acceptance_requirements(public=True))

    def test_public_acceptance_requires_each_original_test_without_skips(self):
        required = FIXTURE.acceptance_requirements(public=True)
        self.assertEqual(len(required), 11)
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in required)
        FIXTURE.verify_acceptance(output, required)
        for name in required:
            with self.subTest(missing=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name), required)

    def test_public_images_reject_missing_control_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            (base / 'public-editor-controls.json').write_text(json.dumps({}))
            with self.assertRaisesRegex(RuntimeError, 'control evidence missing'):
                FIXTURE.verify_public_images(base)

    def test_requires_each_actual_native_test_pass(self):
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.REQUIRED)
        FIXTURE.verify_acceptance(output)
        for name in FIXTURE.REQUIRED:
            with self.subTest(missing=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name))

    def test_similar_names_and_embedded_log_messages_do_not_accept(self):
        for prefix, suffix in [('logged: ', ''), ('', 'Surrogate')]:
            output = '\n'.join(prefix + '--- PASS: ' + name + suffix + ' (0.01s)' for name in FIXTURE.REQUIRED)
            with self.subTest(prefix=prefix, suffix=suffix), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output)

    def test_nested_subtest_passes_do_not_replace_native_acceptance(self):
        output = '\n'.join('    --- PASS: ' + name + '/case (0.01s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_acceptance(output)

    def test_provider_acceptance_requires_original_protocol_and_each_real_workflow(self):
        required = FIXTURE.REQUIRED + FIXTURE.PROVIDER_REQUIRED
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in required)
        FIXTURE.verify_acceptance(output, required)
        for name in required:
            with self.subTest(missing=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name), required)

    def test_original_bridge_passes_do_not_accept_provider_or_workspace(self):
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.REQUIRED)
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_acceptance(output, FIXTURE.REQUIRED + FIXTURE.PROVIDER_REQUIRED)

    def test_private_provider_passes_do_not_accept_public_runtime(self):
        private = FIXTURE.REQUIRED + FIXTURE.PROVIDER_REQUIRED
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in private)
        required = private + FIXTURE.PUBLIC_RUNTIME_REQUIRED
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_acceptance(output, required)
        public = output + '\n' + '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.PUBLIC_RUNTIME_REQUIRED)
        FIXTURE.verify_acceptance(public, required)
        for name in required:
            with self.subTest(missing=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(public.replace('--- PASS: ' + name, '--- SKIP: ' + name), required)


if __name__ == '__main__':
    unittest.main()
