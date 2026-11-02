#!/usr/bin/env python3
"""Run a formal native AI SQL window against an owned local protocol fixture."""
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import secrets
import subprocess
import threading
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, birth, environment, manifest, spawn, terminate, validate


def fixture_handler(token, record, lock):
    """Serve only the owned, explicitly previewed request; never record its secrets."""
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def do_POST(self):
            try:
                size = int(self.headers.get("Content-Length", "0"))
                if not 0 < size <= 512 * 1024:
                    raise ValueError("invalid size")
                body = json.loads(self.rfile.read(size))
                prompt = body["messages"][0]["content"]
                correct = (self.path == "/v1/chat/completions" and
                           self.headers.get("Authorization") == "Bearer " + token and
                           body["model"] == "owned-native-model" and body["stream"] is True and
                           body["store"] is False and body["max_completion_tokens"] == 2048)
                context_only = ("ai-native-fixture" in prompt and "original-ai-draft" not in prompt
                                and "fixture-table-value" not in prompt)
            except (ValueError, KeyError, IndexError, TypeError):
                correct = context_only = False
            with lock:
                record["request_count"] += 1
                record["protocol_verified"] = correct
                record["selected_context_only"] = context_only
            if not correct or not context_only:
                self.send_error(400)
                return
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.end_headers()
            for text in ["SELECT ", "'ai-native-fixture' AS marker;"]:
                data = json.dumps(dict(choices=[dict(delta=dict(content=text), finish_reason=None)]))
                self.wfile.write(("data: " + data + "\n\n").encode())
                self.wfile.flush()
                time.sleep(.15)
            self.wfile.write(b'data: {"choices":[{"delta":{},"finish_reason":"stop"}]}\n\ndata: [DONE]\n\n')
            self.wfile.flush()
    return Handler


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    if "app" in state["processes"] or (root / "config/NyaDB/config.sqlite").exists():
        raise RuntimeError("Native AI replay requires empty owned configuration")
    env = environment(root, state)
    evidence = root / "evidence/ai-sql-native"
    evidence.mkdir()
    token = secrets.token_hex(32)
    record = dict(request_count=0, protocol_verified=False, selected_context_only=False,
                  service_stopped=False, app_stopped=False)
    lock = threading.Lock()
    server = ThreadingHTTPServer(("127.0.0.1", 0), fixture_handler(token, record, lock))
    server.daemon_threads = True
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    env.update(NYADB_AI_NATIVE_OWNED="1", NYADB_AI_NATIVE_KEY=token,
               NYADB_AI_NATIVE_ENDPOINT=f"http://127.0.0.1:{server.server_port}/v1")
    cap = app = None

    def save():
        (evidence / "result.json").write_text(json.dumps(record, indent=2) + "\n")

    try:
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-ai"], cwd=REPO,
                       env=env, check=True, timeout=120)
        binary = REPO / "build/linux-amd64/nyadb"
        binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
        record["binary_sha256"] = binary_hash
        cap = NativeCapture(env["DISPLAY"], evidence, binary)

        def start():
            nonlocal app
            app = spawn(root, state, "app", [str(binary)], env)
            identity = dict(state["processes"]["app"])
            record.setdefault("app_identities", []).append(identity)
            window = cap.wait_window("^NyaDB$", pid=app.pid)
            if hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest() != binary_hash:
                raise RuntimeError("Formal AI binary identity changed")
            cap.move_client(window, 30, 50)
            cap.resize(window, 1280, 800)
            time.sleep(.8)
            return window

        def stop_app():
            if "app" in state["processes"]:
                terminate(state["processes"].pop("app"))
                manifest(root, state)
                app.wait(timeout=10)

        window = start()
        def click(x, y):
            cap.actions.append(dict(action="click", client_x=x, client_y=y, button=1))
            cap.click(window, x, y)
            time.sleep(.3)

        def key(*keys):
            cap.actions.append(dict(action="key", keys=list(keys)))
            cap.key(*keys)
            time.sleep(.3)

        def capture(name):
            cap.capture(name, window, "ai-sql", dict(loading=False, binary_sha256=binary_hash,
                        own_http_fixture=True, real_keyring=True))

        capture("original-draft")
        click(197, 12)
        key("Down", "Return")
        capture("settings")
        key("Escape")
        click(197, 12)
        key("Down", "Down", "Return")
        click(450, 300)
        cap.run("xdotool", "type", "--clearmodifiers", "--delay", "10", "生成一个 SQLite 查询，返回文本 ai-native-fixture。")
        capture("request-preview-1280")
        click(667, 602)
        time.sleep(1)
        capture("suggestion-1280")
        cap.resize(window, 840, 560)
        capture("suggestion-840")
        cap.resize(window, 1280, 800)
        click(770, 602)
        time.sleep(1)
        capture("inserted-draft")
        subprocess.run(["go", "run", "./tools/entryfixture", "native-ai"], cwd=REPO,
                       env=env, check=True, timeout=120)
        stop_app()
        window = start()
        capture("restart-draft")
        click(197, 12)
        key("Down", "Return")
        capture("restart-settings")
        subprocess.run(["go", "run", "./tools/entryfixture", "native-ai"], cwd=REPO,
                       env=env, check=True, timeout=120)
        subprocess.run(["go", "run", "./tools/entryfixture", "native-ai-keyfailures"], cwd=REPO,
                       env=env, check=True, timeout=120)
        record["restart_keyring_verified"] = True
        record["missing_and_wrong_key_preserve_settings"] = True
        with lock:
            if record["request_count"] != 1 or not record["protocol_verified"] or not record["selected_context_only"]:
                raise RuntimeError("Native AI protocol or restart replay checkpoint failed")
        record["capture_exit"] = 0
    finally:
        if cap is not None:
            cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
            manifest(root, state)
            if app is not None:
                app.wait(timeout=10)
        record["app_stopped"] = all(birth(p["pid"]) != p["birth"] for p in record.get("app_identities", []))
        server.shutdown()
        server.server_close()
        thread.join(timeout=10)
        record["service_stopped"] = not thread.is_alive()
        save()


if __name__ == "__main__":
    main()
