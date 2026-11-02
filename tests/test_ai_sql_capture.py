import copy
from http.server import ThreadingHTTPServer
import json
from pathlib import Path
import sys
import threading
import unittest
import urllib.error
import urllib.request

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tools"))
import capture_ai_sql_acceptance as capture


class AISQLCaptureTests(unittest.TestCase):
    def setUp(self):
        self.token = "owned-test-token"
        self.record = dict(request_count=0, protocol_verified=False, selected_context_only=False)
        self.server = ThreadingHTTPServer(("127.0.0.1", 0),
            capture.fixture_handler(self.token, self.record, threading.Lock()))
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start()
        self.addCleanup(self.stop)
        self.body = dict(model="owned-native-model", messages=[dict(role="user",
            content="Generate SQLite SQL returning ai-native-fixture")], stream=True,
            store=False, max_completion_tokens=2048)

    def stop(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join(timeout=5)
        self.assertFalse(self.thread.is_alive())

    def post(self, body, token=None):
        request = urllib.request.Request(
            f"http://127.0.0.1:{self.server.server_port}/v1/chat/completions",
            data=json.dumps(body).encode(), headers={"Authorization": "Bearer " +
                (self.token if token is None else token), "Content-Type": "application/json"})
        with urllib.request.urlopen(request, timeout=5) as response:
            return response.read().decode()

    def test_owned_protocol_stream_finishes_without_recording_prompt_or_key(self):
        response = self.post(self.body)
        self.assertIn("finish_reason", response)
        self.assertTrue(response.endswith("data: [DONE]\n\n"))
        self.assertEqual(self.record, dict(request_count=1, protocol_verified=True,
                                        selected_context_only=True))
        serialized = json.dumps(self.record)
        self.assertNotIn(self.token, serialized)
        self.assertNotIn(self.body["messages"][0]["content"], serialized)

    def test_private_unselected_sql_or_table_values_fail_acceptance(self):
        for private in ("original-ai-draft", "fixture-table-value"):
            body = copy.deepcopy(self.body)
            body["messages"][0]["content"] += private
            with self.assertRaises(urllib.error.HTTPError) as result:
                self.post(body)
            self.assertEqual(result.exception.code, 400)
            self.assertFalse(self.record["selected_context_only"])

    def test_wrong_key_protocol_or_malformed_body_never_passes(self):
        variants = [(self.body, "wrong-key"), ({}, None)]
        for field, value in (("store", True), ("stream", False),
                             ("model", "wrong-model"), ("max_completion_tokens", 1)):
            body = copy.deepcopy(self.body)
            body[field] = value
            variants.append((body, None))
        for body, token in variants:
            with self.assertRaises(urllib.error.HTTPError) as result:
                self.post(body, token)
            self.assertEqual(result.exception.code, 400)
            self.assertFalse(self.record["protocol_verified"])


if __name__ == "__main__":
    unittest.main()
