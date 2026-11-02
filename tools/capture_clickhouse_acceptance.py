#!/usr/bin/env python3
"""Capture an owned native ClickHouse service, keyring and formal Linux window.

Invoke with verify_entry_acceptance.py in a fresh --start-only owned run.
This script creates and removes only its own labelled ClickHouse container.
"""
import hashlib
import json
import os
from pathlib import Path
import secrets
import subprocess
import time

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, environment, manifest, spawn, terminate, validate


def main():
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    env = environment(root, state)
    if "app" in state["processes"] or (root / "config/NyaDB/config.sqlite").exists():
        raise RuntimeError("Native ClickHouse replay requires empty owned configuration")
    run_id = "nyadb-ch-native-" + state["id"] + "-" + secrets.token_hex(4)
    container = None
    password = secrets.token_hex(32)
    env_file = root / "clickhouse-credentials.env"
    env_file.touch(mode=0o600, exist_ok=False)
    env_file.write_text("CLICKHOUSE_USER=nyadb_it\nCLICKHOUSE_PASSWORD=" + password
                        + "\nCLICKHOUSE_DEFAULT_ACCESS_MANAGEMENT=1\n")
    evidence = root / "evidence/clickhouse-native"
    evidence.mkdir()
    resource = dict(run_id=run_id, container_id=None, owned_container_removed=False)
    cap = None

    def docker(*args):
        result = subprocess.run(["docker", *args], capture_output=True, text=True, timeout=120)
        if result.returncode:
            raise RuntimeError("Owned ClickHouse container command failed")
        return result.stdout.strip()

    def record():
        (evidence / "resource.json").write_text(json.dumps(resource, indent=2) + "\n")

    try:
        container = docker("create", "--name", run_id, "--label", "org.nyadb.clickhouse-native=" + run_id,
                           "-p", "127.0.0.1::9000", "--env-file", str(env_file),
                           "--ulimit", "nofile=262144:262144", "clickhouse/clickhouse-server:25.8")
        resource["container_id"] = container
        record()
        docker("start", container)
        for _ in range(90):
            ready = subprocess.run(["docker", "exec", container, "bash", "-c",
                                    "exec 3<>/dev/tcp/127.0.0.1/9000"], capture_output=True, timeout=5)
            if ready.returncode == 0:
                break
            time.sleep(.5)
        else:
            raise RuntimeError("Owned ClickHouse fixture did not start")
        port = docker("port", container, "9000/tcp").rsplit(":", 1)[1]
        resource["server_version"] = docker("exec", container, "clickhouse-server", "--version")
        resource["image_id"] = docker("inspect", "--format", "{{.Image}}", container)
        env.update(NYADB_CLICKHOUSE_PORT=port, NYADB_CLICKHOUSE_PASSWORD=password,
                   NYADB_CLICKHOUSE_NATIVE_OWNED="1")
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-clickhouse"], cwd=REPO,
                       env=env, check=True, timeout=120)
        binary = REPO / "build/linux-amd64/nyadb"
        binary_hash = hashlib.sha256(binary.read_bytes()).hexdigest()
        resource["binary_sha256"] = binary_hash
        cap = NativeCapture(env["DISPLAY"], evidence, binary)
        spawn(root, state, "app", [str(binary)], env)
        manifest(root, state)
        pid = state["processes"]["app"]["pid"]
        window = cap.wait_window("^NyaDB$", pid=pid)
        if hashlib.sha256(Path(f"/proc/{pid}/exe").read_bytes()).hexdigest() != binary_hash:
            raise RuntimeError("Running formal application hash changed")
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)

        def click(x, y, target=window, button=1):
            cap.actions.append(dict(action="click", client_x=x, client_y=y, button=button))
            cap.click(target, x, y, button=button)
            time.sleep(.35)

        def key(*keys):
            cap.actions.append(dict(action="key", keys=list(keys)))
            cap.key(*keys)
            time.sleep(.35)

        def capture(name, target=window):
            cap.capture(name, target, "clickhouse", dict(loading=False, binary_sha256=binary_hash,
                        own_service=True, real_keyring=True))

        capture("profiles-1280")
        click(120, 128, button=3)
        key("Down", "Down", "Return")
        child = cap.wait_window(r"^编辑连接 \(ClickHouse\)$", pid=pid)
        cap.move_client(child, 300, 120)
        capture("connection-general", child)
        click(75, 14, child)
        capture("connection-tls", child)
        cap.resize(child, 720, 520)
        capture("connection-tls-720", child)
        key("Escape")
        click(120, 128)
        key("Return")
        time.sleep(1)
        capture("connected-namespaces")
        # Default namespace follows INFORMATION_SCHEMA in the server snapshot.
        click(125, 171)
        key("Return")
        time.sleep(.8)
        capture("objects")
        click(350, 190)
        key("Return")
        time.sleep(.8)
        capture("grid-1280")
        cap.resize(window, 840, 560)
        capture("grid-840")
        cap.resize(window, 1280, 800)
        click(390, 109)
        capture("restored-sql")
        click(575, 165)
        time.sleep(.8)
        capture("recovered-sql")
        click(279, 138)
        time.sleep(.8)
        capture("sql-result")
        subprocess.run(["go", "run", "./tools/entryfixture", "native-clickhouse"], cwd=REPO,
                       env=env, check=True, timeout=120)
        resource["capture_exit"] = 0
        record()
    finally:
        if cap is not None:
            cap.close()
        if "app" in state["processes"]:
            terminate(state["processes"].pop("app"))
            manifest(root, state)
        try:
            if container:
                label = docker("inspect", "--format", '{{index .Config.Labels "org.nyadb.clickhouse-native"}}', container)
                if label == run_id:
                    docker("rm", "-f", container)
                remaining = docker("ps", "--all", "--no-trunc", "--filter", "id=" + container,
                                   "--format", "{{.ID}}")
                resource["owned_container_removed"] = not remaining
        finally:
            env_file.unlink(missing_ok=True)
            record()


if __name__ == "__main__":
    main()
