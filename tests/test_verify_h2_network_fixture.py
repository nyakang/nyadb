import importlib.util
from pathlib import Path
import sys
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
SPEC = importlib.util.spec_from_file_location("h2_network_fixture", TOOLS / "verify_h2_network_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class H2NetworkAcceptanceEvidenceTests(unittest.TestCase):
    def test_each_original_version_and_transport_is_required(self):
        for name, verify in [
            ("TestH2NativeTCPAndTLSAuthenticationDataCancellationAndRecovery", FIXTURE.verify_network_output),
            ("TestH2NativeNetworkPublicGridSQLAndExplicitRecovery", FIXTURE.verify_shell_output),
        ]:
            lines = ["--- PASS: " + name + suffix + " (1.00s)" for suffix in [""] + ["/" + profile + "/" + mode for profile in FIXTURE.PROFILES for mode in ["tcp", "ssl"]]]
            verify("\n".join(lines))
            for missing in range(len(lines)):
                with self.subTest(name=name, missing=missing), self.assertRaises(RuntimeError):
                    verify("\n".join(line for i, line in enumerate(lines) if i != missing))


if __name__ == "__main__":
    unittest.main()
