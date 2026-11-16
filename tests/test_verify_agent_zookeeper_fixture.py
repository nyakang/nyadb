import importlib.util
import base64
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


SPEC = importlib.util.spec_from_file_location('zookeeper_fixture', Path(__file__).resolve().parents[1] / 'tools/verify_agent_zookeeper_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class ZooKeeperAcceptanceEvidenceTests(unittest.TestCase):
    def test_cleanup_requires_original_ownership_and_removes_anonymous_volumes(self):
        inspected = {'Id': 'original', 'Config': {'Labels': {'nyadb.zookeeper.fixture': 'owned'}},
            'Mounts': [{'Type': 'volume', 'Name': 'original-volume'}, {'Type': 'bind', 'Source': '/security'}]}
        responses = [json.dumps([inspected]).encode(), b'original logs', b'original', b'foreign-volume\n']
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(FIXTURE, 'run', side_effect=responses) as run:
            self.assertEqual(FIXTURE.cleanup_owned_container(Path(directory), 'server', 'original', 'owned'), 1)
            self.assertEqual(run.call_args_list, [mock.call(['docker', 'inspect', 'original']),
                mock.call(['docker', 'logs', 'original']), mock.call(['docker', 'rm', '-f', '-v', 'original']),
                mock.call(['docker', 'volume', 'ls', '-q'])])
        for change in ['ownership', 'identity', 'remaining-volume']:
            invalid = copy.deepcopy(inspected)
            if change == 'ownership':
                invalid['Config']['Labels']['nyadb.zookeeper.fixture'] = 'foreign'
            elif change == 'identity':
                invalid['Id'] = 'replacement'
            responses = [json.dumps([invalid]).encode(), b'logs', b'original', b'original-volume\n']
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory, mock.patch.object(FIXTURE, 'run', side_effect=responses) as run:
                with self.assertRaises(RuntimeError):
                    FIXTURE.cleanup_owned_container(Path(directory), 'server', 'original', 'owned')
                if change != 'remaining-volume':
                    run.assert_called_once_with(['docker', 'inspect', 'original'])

    def test_ensemble_restart_requires_original_ports_members_and_real_roles(self):
        topology = {'endpoints': ['127.0.0.1:12001','127.0.0.1:12002','127.0.0.1:12003'], 'containers': ['member1','member2','member3'], 'token': 'owned'}
        observed = [{'Id': cid, 'Config': {'Labels': {'nyadb.zookeeper.fixture': 'owned'}}, 'State': {'Running': True},
            'NetworkSettings': {'Ports': {'2181/tcp': [{'HostIp': '127.0.0.1', 'HostPort': str(12001+i)}]}}} for i,cid in enumerate(topology['containers'])]
        roles = ['leader','follower','follower']
        FIXTURE.verify_ensemble_observations(topology, observed, roles)
        for change in ['port','member','ownership','stopped','quorum']:
            with self.subTest(change=change):
                invalid = copy.deepcopy(observed)
                new_roles = list(roles)
                if change == 'port':
                    invalid[0]['NetworkSettings']['Ports']['2181/tcp'][0]['HostPort'] = '12004'
                elif change == 'member':
                    invalid[0]['Id'] = 'replacement'
                elif change == 'ownership':
                    invalid[0]['Config']['Labels']['nyadb.zookeeper.fixture'] = 'foreign'
                elif change == 'stopped':
                    invalid[0]['State']['Running'] = False
                else:
                    new_roles[0] = 'standalone'
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_ensemble_observations(topology, invalid, new_roles)

    def topology_receipt_fixture(self, base):
        records = self.security_receipt_fixture(base)
        combined = {}
        for mode in ['digest-tls', 'digest-mtls', 'sasl-tls', 'sasl-mtls']:
            data = copy.deepcopy(records['sasl'])
            data.pop('rejected')
            data['wrong_ca_rejected'] = True
            if mode.endswith('mtls'):
                data['missing_client_rejected'] = True
            if mode.startswith('digest'):
                data['denied'] = copy.deepcopy(records['digest']['denied'][0])
            else:
                data['rejected'] = {'original_process_starts': 1, 'test_deadline_expired': False, 'error': 'original secure authentication rejected'}
            combined[mode] = data
        ensemble = copy.deepcopy(records['sasl'])
        ensemble.pop('read')
        ensemble.pop('rejected')
        ensemble.update({'root': copy.deepcopy(records['sasl']['read']), 'created': copy.deepcopy(ensemble['write']),
            'before': copy.deepcopy(ensemble['verified']), 'after': copy.deepcopy(ensemble['verified']),
            'closed': copy.deepcopy(ensemble['verified']), 'ephemeral_owner': 17948111111,
            'original_pid': 4411, 'failed_member': 0, 'surviving_member': 2,
            'original_process_starts_during_failover': 0, 'root_identity': '/nyadb_ensemble/ns'})
        for name in ['before', 'after']:
            ensemble[name]['Result']['ZNode']['Reply']['Node']['Stat']['ephemeralOwner'] = ensemble['ephemeral_owner']
        ensemble['closed']['Result']['ZNode']['Reply']['Found'] = False
        for data in [ensemble, *combined.values()]:
            data['verified']['Result']['ZNode']['Reply']['Node']['Stat']['version'] = 0
        combined['ensemble'] = ensemble
        # Fixture envelopes carry the same original get fields as their typed
        # projections. Alterations below must not invent a new native receipt.
        for data in combined.values():
            for receipt in data.values():
                if not isinstance(receipt, dict) or 'Result' not in receipt or receipt['Result']['Request']['Method'] != 'kv_get' or receipt['Result']['Code'] != 'OK':
                    continue
                result = receipt['Result']
                reply = result['ZNode']['Reply']
                native = json.loads(base64.b64decode(receipt['NativeBytes']))
                native['result']['found'] = reply['Found']
                if reply['Found']:
                    native['result']['value']['data'] = reply['Node']['Value']
                    native['result']['metadata'] = copy.deepcopy(reply['Node']['Stat'])
                else:
                    native['result']['value'], native['result']['metadata'] = None, None
                raw = json.dumps(native).encode()
                receipt['NativeBytes'], receipt['NativeSHA256'], result['JSON'] = base64.b64encode(raw).decode(), FIXTURE.digest(raw), json.dumps(native['result'])
        for mode,data in combined.items():
            (base / f'topology-{mode}-receipts.json').write_text(json.dumps(data))
        return combined

    def test_security_and_foundation_tests_cannot_accept_topology(self):
        required = FIXTURE.acceptance_requirements(topology=True)
        self.assertEqual(len(required), 17)
        self.assertTrue(set(FIXTURE.SECURITY_REQUIRED).issubset(required))
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in FIXTURE.acceptance_requirements(security=True))
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_acceptance(output, required)
        output = '\n'.join('--- PASS: ' + name + ' (0.01s)' for name in required)
        FIXTURE.verify_acceptance(output, required)
        for name in FIXTURE.TOPOLOGY_REQUIRED:
            with self.subTest(skipped=name), self.assertRaises(RuntimeError):
                FIXTURE.verify_acceptance(output.replace('--- PASS: ' + name, '--- SKIP: ' + name), required)

    def test_topology_receipts_require_all_real_combinations_and_original_failover(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            self.topology_receipt_fixture(base)
            self.assertEqual(len(FIXTURE.verify_topology_receipts(base)), 5)
            (base / 'topology-sasl-mtls-receipts.json').unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_topology_receipts(base)

    def test_topology_receipts_reject_replaced_sessions_replayed_writes_and_false_close(self):
        for change in ['owner', 'same-member', 'replacement-process', 'qualified-root', 'replay-version', 'not-closed', 'missing-native']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                records = self.topology_receipt_fixture(base)
                ensemble = records['ensemble']
                if change == 'owner':
                    ensemble['after']['Result']['ZNode']['Reply']['Node']['Stat']['ephemeralOwner'] += 1
                elif change == 'same-member':
                    ensemble['surviving_member'] = ensemble['failed_member']
                elif change == 'replacement-process':
                    ensemble['original_process_starts_during_failover'] = 1
                elif change == 'qualified-root':
                    ensemble['root_identity'] = '/unrelated'
                elif change == 'replay-version':
                    ensemble['verified']['Result']['ZNode']['Reply']['Node']['Stat']['version'] = 1
                elif change == 'not-closed':
                    ensemble['closed']['Result']['ZNode']['Reply']['Found'] = True
                else:
                    del ensemble['after']['NativeBytes']
                (base / 'topology-ensemble-receipts.json').write_text(json.dumps(ensemble))
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_topology_receipts(base)

    def test_topology_receipts_reject_unproven_secure_authentication(self):
        for change in ['wrong-ca', 'missing-client', 'digest-acl', 'sasl-deadline']:
            with self.subTest(change=change), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                records = self.topology_receipt_fixture(base)
                mode = 'digest-mtls' if change != 'sasl-deadline' else 'sasl-tls'
                data = records[mode]
                if change == 'wrong-ca':
                    data['wrong_ca_rejected'] = False
                elif change == 'missing-client':
                    data['missing_client_rejected'] = False
                elif change == 'digest-acl':
                    del data['denied']
                else:
                    data['rejected']['test_deadline_expired'] = True
                (base / f'topology-{mode}-receipts.json').write_text(json.dumps(data))
                with self.assertRaises(RuntimeError):
                    FIXTURE.verify_topology_receipts(base)

    def test_topology_images_require_all_stages_locales_and_sizes(self):
        from PIL import Image
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            strip = Image.new('RGB', (32, 1))
            strip.putdata([(i * 7, i * 3, i * 5) for i in range(32)])
            for language in ['zh-Hans', 'en']:
                for width, height in [(1280, 800), (720, 520)]:
                    for stage in ['ensemble-failover', 'digest-tls', 'digest-mtls', 'sasl-tls', 'sasl-mtls']:
                        strip.resize((width,height)).save(base / f'{language}-{width}x{height}-security-{stage}.png')
            self.assertEqual(len(FIXTURE.verify_topology_images(base)), 20)
            (base / 'en-720x520-security-ensemble-failover.png').unlink()
            with self.assertRaises(FileNotFoundError):
                FIXTURE.verify_topology_images(base)

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
            envelope['error' if denied else 'result'] = {'code': -1, 'message': 'zk: not authenticated'} if denied else {'found': True, 'key': '/fixture', 'value': {'encoding': 'base64', 'data': base64.b64encode(value).decode()}, 'metadata': {'version': 1, 'ephemeralOwner': 0}} if method == 'kv_get' else {'ok': True}
            raw = json.dumps(envelope).encode()
            result = {'Request': {'Method': method}, 'Attempted': True, 'Completed': not denied,
                'WriteOutcomeUnknown': denied and method == 'kv_put', 'Code': 'NativeError' if denied else 'OK',
                'ProjectionError': '', 'JSON': '' if denied else json.dumps(envelope['result']),
                'ZNode': {'Captured': True, 'Acknowledged': True, 'ErrorCode': -1 if denied else None,
                    'Reply': {'Found': True, 'Node': {'Value': base64.b64encode(value).decode(), 'Stat': {'version': 1, 'ephemeralOwner': 0}}}}}
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
