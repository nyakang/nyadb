import importlib.util
from pathlib import Path
import unittest


SPEC = importlib.util.spec_from_file_location(
    "h2_fixture", Path(__file__).resolve().parents[1] / "tools/verify_agent_h2_fixture.py"
)
FIXTURE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(FIXTURE)


class H2EditorAcceptanceEvidenceTests(unittest.TestCase):
    def test_requires_original_native_test_and_every_profile(self):
        name = "TestH2NativeEditorDriverImportPersistenceCredentialsAndIsolatedTesting"
        lines = [
            "--- PASS: " + name + suffix + " (1.00s)"
            for suffix in ["", "/h2-v1", "/h2-v2", "/h2-v3", "/h2-legacy"]
        ]
        FIXTURE.verify_editor_output("\n".join(lines))
        for removed in range(len(lines)):
            with self.subTest(removed=removed), self.assertRaises(RuntimeError):
                FIXTURE.verify_editor_output(
                    "\n".join(line for i, line in enumerate(lines) if i != removed)
                )

    def test_skipped_or_generic_pass_is_insufficient(self):
        with self.assertRaises(RuntimeError):
            FIXTURE.verify_editor_output(
                "--- SKIP: TestH2NativeEditorDriverImportPersistenceCredentialsAndIsolatedTesting (0.00s)\nPASS"
            )

    def test_public_shell_requires_each_original_profile(self):
        name = "TestH2NativePublicChooserURISharedGridSQLStructureAndRecovery"
        lines = ["--- PASS: " + name + suffix + " (1.00s)" for suffix in ["", "/h2-v1", "/h2-v2", "/h2-v3", "/h2-legacy"]]
        FIXTURE.verify_shell_output("\n".join(lines))
        for missing in range(len(lines)):
            with self.subTest(missing=missing), self.assertRaises(RuntimeError):
                FIXTURE.verify_shell_output("\n".join(line for i, line in enumerate(lines) if i != missing))


if __name__ == "__main__":
    unittest.main()
