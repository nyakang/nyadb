import importlib.util
import base64
import copy
import json
from pathlib import Path
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location('zookeeper_fixture', Path(__file__).resolve().parents[1] / 'tools/verify_agent_zookeeper_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class ZooKeeperAcceptanceEvidenceTests(unittest.TestCase):
    def security_image_fixture(self, base):
        from PIL import Image
        strip = Image.new('RGB', (32, 1))
        strip.putdata([(i * 7, i * 3, i * 5) for i in range(32)])
        for language in ['zh-Hans', 'en']:
            for width, height in [(920, 780), (720, 520)]:
                for mode in ['tls', 'mtls', 'sasl']:
                    strip.resize((width, height)).save(base / f'{language}-{width}x{height}-security-editor-{mode}.png')
            for width, height in [(1280, 800), (720, 520)]:
                for stage in ['digest-denied-write', 'tls-recovery', 'mtls-recovery', 'sasl-receipt']:
                    strip.resize((width, height)).save(base / f'{language}-{width}x{height}-security-{stage}.png')

    def security_receipt_fixture(self, base):
        def receipt(method, denied=False, value=b'fixture'):
            envelope = {'jsonrpc': '2.0', 'id': 7}
            envelope['error' if denied else 'result'] = {'code': -1, 'message': 'zk: not authenticated'} if denied else {'ok': True}
            raw = json.dumps(envelope).encode()
            result = {'Request': {'Method': method}, 'Attempted': True, 'Completed': not denied,
                'WriteOutcomeUnknown': denied and method == 'kv_put', 'Code': 'NativeError' if denied else 'OK',
                'ProjectionError': '', 'JSON': '' if denied else json.dumps(envelope['result']),
                'ZNode': {'Captured': True, 'Acknowledged': True, 'ErrorCode': -1 if denied else None,
                    'Reply': {'Node': {'Value': base64.b64encode(value).decode(), 'Stat': {'version': 1}}}}}
            return {'Result': result, 'NativeBytes': base64.b64encode(raw).decode(), 'NativeSHA256': FIXTURE.digest(raw)}
        records = {}
        for mode in ['digest', 'tls', 'mtls', 'sasl']:
            value = b'original-authenticated-value' if mode == 'digest' else bytes([0, 255, 128])
            data = {'read': receipt('kv_get'), 'write': receipt('kv_put'), 'verified': receipt('kv_get', value=value)}
            if mode == 'digest':
                data['denied'] = [receipt(method, denied=True) for method in ['kv_get', 'kv_put', 'kv_get', 'kv_put']]
            else:
                data['generation'] = 3
            if mode in ['tls', 'mtls']:
                data['original'] = receipt('kv_get')
                data['recovered'] = copy.deepcopy(data['original'])
            if mode == 'sasl':
                data['rejected'] = [{'mode': probe, 'error': 'original authentication rejected',
                    'original_process_starts': 1, 'test_deadline_expired': False} for probe in ['wrong-secret', 'none', 'digest']]
            records[mode] = data
            (base / f'security-{mode}-receipts.json').write_text(json.dumps(data))
        return records

    def test_foundation_and_public_passes_cannot_accept_security(self):
        required = FIXTURE.acceptance_requirements(security=True)
        self.assertEqual(len(required), 15)
        for flags in [{}, {'provider': True}, {'public_runtime': True}, {'public': True}]:
            output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.acceptance_requirements(**flags))
            with self.subTest(flags=flags), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output, required)

    def test_security_acceptance_requires_each_real_test_without_skips(self):
        required = FIXTURE.acceptance_requirements(security=True)
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in required)
        FIXTURE.verify_acceptance(output, required)
        for name in FIXTURE.SECURITY_REQUIRED:
            with self.subTest(skipped=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name), required)

    def test_security_images_require_all_modes_locales_and_sizes(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            self.security_image_fixture(base)
            self.assertEqual(len(FIXTURE.verify_security_images(base)), 28)
            (base / 'en-720x520-security-editor-mtls.png').unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_security_images(base)

    def test_security_images_reject_blank_or_wrong_dimensions(self):
        from PIL import Image
        for dimensions, message in [((720, 520), 'rendered UI pixels'), ((720, 519), 'dimensions differ')]:
            with self.subTest(dimensions=dimensions), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                self.security_image_fixture(base)
                Image.new('RGB', dimensions, 'black').save(base / 'en-720x520-security-tls-recovery.png')
                with self.assertRaisesRegex(RuntimeError, message):
                    FIXTURE.verify_security_images(base)

    def test_security_receipts_require_original_bytes_and_all_files(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            records = self.security_receipt_fixture(base)
            self.assertEqual(len(FIXTURE.verify_security_receipts(base)), 4)
            del records['digest']['read']['NativeBytes']
            (base / 'security-digest-receipts.json').write_text(json.dumps(records['digest']))
            with self.assertRaisesRegex(RuntimeError, 'original native bytes'):
                FIXTURE.verify_security_receipts(base)
            self.security_receipt_fixture(base)
            (base / 'security-mtls-receipts.json').unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_security_receipts(base)

    def test_security_receipts_reject_malformed_altered_and_unrelated_native(self):
        for change in ['base64', 'json', 'hash', 'unrelated-result', 'non-acl-error', 'lost-unknown', 'credential']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                records = self.security_receipt_fixture(base)
                receipt = records['digest']['denied'][1]
                native = json.loads(base64.b64decode(receipt['NativeBytes']))
                if change == 'base64':
                    receipt['NativeBytes'] = 'not base64!'
                elif change == 'json':
                    receipt['NativeBytes'] = base64.b64encode(b'{invalid').decode()
                elif change == 'hash':
                    receipt['NativeSHA256'] = '0' * 64
                elif change == 'unrelated-result':
                    records['digest']['read']['Result']['JSON'] = '{"unrelated":true}'
                elif change == 'lost-unknown':
                    receipt['Result']['WriteOutcomeUnknown'] = False
                else:
                    native['error']['message'] = 'connection refused' if change == 'non-acl-error' else 'owned-digest-secret'
                    raw = json.dumps(native).encode()
                    receipt['NativeBytes'], receipt['NativeSHA256'] = base64.b64encode(raw).decode(), FIXTURE.digest(raw)
                (base / 'security-digest-receipts.json').write_text(json.dumps(records['digest']))
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_security_receipts(base)

    def test_security_recovery_requires_identical_original_envelope(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            records = self.security_receipt_fixture(base)
            receipt = records['tls']['recovered']
            native = json.loads(base64.b64decode(receipt['NativeBytes']))
            native['id'] += 1
            raw = json.dumps(native).encode()
            receipt['NativeBytes'], receipt['NativeSHA256'] = base64.b64encode(raw).decode(), FIXTURE.digest(raw)
            (base / 'security-tls-receipts.json').write_text(json.dumps(records['tls']))
            with self.assertRaisesRegex(RuntimeError, 'replaced original receipt bytes'):
                FIXTURE.verify_security_receipts(base)

    def test_security_sasl_rejects_skipped_fallback_or_deadline_only_probes(self):
        for change in ['missing', 'no-original-process', 'deadline']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                records = self.security_receipt_fixture(base)
                probes = records['sasl']['rejected']
                if change == 'missing':
                    probes.pop()
                elif change == 'no-original-process':
                    probes[0]['original_process_starts'] = 0
                else:
                    probes[0]['test_deadline_expired'] = True
                (base / 'security-sasl-receipts.json').write_text(json.dumps(records['sasl']))
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_security_receipts(base)

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
