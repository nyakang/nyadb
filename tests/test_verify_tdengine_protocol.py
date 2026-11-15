import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "verify_tdengine_protocol", Path(__file__).resolve().parents[1] / "tools/verify_tdengine_protocol.py"
)
GATE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(GATE)


class TDengineProtocolEvidenceTests(unittest.TestCase):
    def test_skipped_native_test_cannot_count_as_accepted(self):
        required = "TestNativeTDengineExactTypesNullTextBinaryAndTimestamp"
        log = '\n'.join([
            '{"Action":"skip","Test":"' + required + '"}',
            '{"Action":"pass","Package":"fixture"}',
            '{"Action":"pass","Test":"TestExactNativeValueEncoding"}',
            'not a JSON event',
        ])
        accepted = GATE.passed_tests(log)
        self.assertNotIn(required, accepted)
        self.assertIn(required, GATE.REQUIRED_TESTS - accepted)

    def test_partial_success_cannot_hide_a_missing_original_receipt_gate(self):
        missing = "TestNativeTDengineLostOriginalWriteAcknowledgementDoesNotReconnect"
        log = '\n'.join('{"Action":"pass","Test":"' + name + '"}' for name in GATE.REQUIRED_TESTS if name != missing)
        self.assertEqual(GATE.REQUIRED_TESTS - GATE.passed_tests(log), {missing})

    def test_cleanup_requires_both_immutable_identity_and_original_owner(self):
        state = {"Id": "original-id", "Config": {"Labels": {"nyadb.owner": "original-owner"}}}
        self.assertTrue(GATE.owned_container(state, "original-id", "original-owner"))
        self.assertFalse(GATE.owned_container(state, "replacement-id", "original-owner"))
        self.assertFalse(GATE.owned_container(state, "original-id", "replacement-owner"))
        self.assertFalse(GATE.owned_container({"Id": "original-id"}, "original-id", "original-owner"))


if __name__ == "__main__":
    unittest.main()
