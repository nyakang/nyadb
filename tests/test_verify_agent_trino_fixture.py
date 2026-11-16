import importlib.util
from pathlib import Path
import unittest


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


if __name__ == "__main__":
    unittest.main()
