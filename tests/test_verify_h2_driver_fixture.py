import importlib.util
from pathlib import Path
import sys
import unittest

TOOLS = Path(__file__).resolve().parents[1] / "tools"
sys.path.insert(0, str(TOOLS))
SPEC = importlib.util.spec_from_file_location("h2_driver_fixture", TOOLS / "verify_h2_driver_fixture.py")
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class H2DriverAcceptanceEvidenceTests(unittest.TestCase):
    def test_each_original_mode_version_and_provider_rejection_is_required(self):
        for shell in [False, True]:
            name = ("TestH2NativeAutomaticAndCustomPublicDriverWorkflows" if shell else
                    "TestH2NativeAutomaticAndCustomDriverOwnership")
            suffixes = [""] + ["/" + mode + "/" + profile
                               for mode in ["auto", "custom"] for profile in FIXTURE.PROFILES]
            if not shell:
                suffixes += ["/page-store", "/new-file", "/auto-rejections"]
            lines = ["--- PASS: " + name + suffix + " (1.00s)" for suffix in suffixes]
            FIXTURE.verify_driver_output("\n".join(lines), shell)
            for missing in range(len(lines)):
                with self.subTest(shell=shell, missing=missing), self.assertRaises(RuntimeError):
                    FIXTURE.verify_driver_output("\n".join(line for i, line in enumerate(lines) if i != missing), shell)
            for failure in ["FAIL\n", "--- FAIL: unrelated (0.00s)\n"]:
                with self.subTest(shell=shell, failure=failure), self.assertRaises(RuntimeError):
                    FIXTURE.verify_driver_output(failure + "\n".join(lines), shell)


if __name__ == "__main__":
    unittest.main()
