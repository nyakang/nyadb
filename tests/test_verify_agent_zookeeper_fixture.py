import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location('zookeeper_fixture', Path(__file__).resolve().parents[1] / 'tools/verify_agent_zookeeper_fixture.py')
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class ZooKeeperAcceptanceEvidenceTests(unittest.TestCase):
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


if __name__ == '__main__':
    unittest.main()
