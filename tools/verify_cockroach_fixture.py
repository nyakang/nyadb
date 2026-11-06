#!/usr/bin/env python3
"""Owned CockroachDB fixture with verified TLS and isolated credentials."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "cockroachdb/cockroach:v25.2.4"


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def remove_owned(name, nonce):
    result = subprocess.run(["docker", "container", "inspect", name], capture_output=True, text=True, check=False)
    if result.returncode:
        return
    try:
        item = json.loads(result.stdout)[0]
        labels = item.get("Config", {}).get("Labels", {}) or {}
        if labels.get("nyadb.fixture") != "cockroach" or labels.get("nyadb.fixture.nonce") != nonce:
            return
        identifier = item["Id"]
        if not isinstance(identifier, str) or not identifier:
            return
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return
    subprocess.run(["docker", "rm", "-f", identifier], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify(hold=False):
    nonce = uuid.uuid4().hex[:12]
    name = "nyadb-cockroach-it-" + nonce
    base = ROOT / "temp" / "cockroach"
    base.mkdir(parents=True, exist_ok=True)
    out = ROOT / "build" / "cockroach"
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        password = secrets.token_hex(24)
        cert = ["docker", "run", "--rm", "-v", str(folder)+":/fixture", IMAGE, "cert"]
        for args in (["create-ca"], ["create-node", "localhost", "127.0.0.1", name], ["create-client", "root"]):
            run(cert + args + ["--certs-dir=/fixture/certs", "--ca-key=/fixture/ca.key"], stdout=subprocess.DEVNULL)
        (folder / "init.sql").write_text("CREATE DATABASE nyadb_cockroach_it; CREATE USER nyadb_it WITH PASSWORD '" + password + "'; GRANT ALL ON DATABASE nyadb_cockroach_it TO nyadb_it; GRANT ALL ON SCHEMA nyadb_cockroach_it.public TO nyadb_it; SET DATABASE = nyadb_cockroach_it; CREATE TABLE records(id INT8 PRIMARY KEY, note STRING, amount DECIMAL(40,20)); INSERT INTO records VALUES(1,'alpha',9007199254740993.12345678901234567890),(2,'beta',2); GRANT ALL ON TABLE records TO nyadb_it; CREATE SEQUENCE fixture_sequence START 987; GRANT ALL ON SEQUENCE fixture_sequence TO nyadb_it; CREATE SEQUENCE private_sequence START 321;")
        (folder / "init.sql").chmod(0o600)
        try:
            run(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=cockroach", "--label", "nyadb.fixture.nonce="+nonce, "--memory=2g", "-p", "127.0.0.1::26257", "-v", str(folder)+":/fixture", IMAGE, "start", "--join=localhost:26258", "--advertise-addr=localhost:26258", "--certs-dir=/fixture/certs", "--store=type=mem,size=768MiB", "--cache=128MiB", "--max-sql-memory=128MiB", "--listen-addr=localhost:26258", "--sql-addr=0.0.0.0:26257", "--http-addr=127.0.0.1:8080"], stdout=subprocess.DEVNULL)
            sql = ["docker", "exec", name, "cockroach", "sql", "--certs-dir=/fixture/certs", "--host=localhost:26257"]
            deadline = time.monotonic()+120
            initialized = False
            while True:
                if not initialized:
                    init = subprocess.run(["docker", "exec", name, "cockroach", "init", "--certs-dir=/fixture/certs", "--host=localhost:26258"], capture_output=True, text=True, check=False)
                    initialized = init.returncode == 0
                result = subprocess.run(sql + ["-e", "SELECT 1"], capture_output=True, text=True, check=False)
                if result.returncode == 0:
                    break
                state = json.loads(run(["docker", "inspect", name], capture_output=True).stdout)[0]["State"]
                if not state["Running"]:
                    logs = run(["docker", "logs", "--tail=20", name], capture_output=True)
                    raise RuntimeError("CockroachDB container exited before bootstrap: " + logs.stderr[-2000:])
                if time.monotonic() > deadline:
                    raise RuntimeError("CockroachDB bootstrap timed out")
                time.sleep(1)
            run(sql + ["--file=/fixture/init.sql"], stdout=subprocess.DEVNULL)
            env = os.environ.copy()
            env.update(CGO_ENABLED="0", GOWORK="off", NYADB_COCKROACH_IT="1", NYADB_COCKROACH_PORT=run(["docker", "port", name, "26257/tcp"], capture_output=True).stdout.strip().rsplit(":",1)[1], NYADB_COCKROACH_PASSWORD=password, NYADB_COCKROACH_CA_FILE=str(folder / "certs/ca.crt"), NYADB_COCKROACH_CONTAINER=name, NYADB_COCKROACH_VISUAL_DIR=str(out))
            path = folder / "environment.json"
            path.write_text(json.dumps({k:v for k,v in env.items() if k.startswith("NYADB_COCKROACH_")}))
            path.chmod(0o600)
            print("CockroachDB fixture ready; private environment file:", path, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_COCKROACH_RACE") == "1"
            if race:
                env["CGO_ENABLED"] = "1"
            label = "race" if race else "tcp-tls"
            with (out / f"{label}-verification.log").open("w") as log:
                run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/engines", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^TestCockroach", "-count=1", "-timeout=240s", "-v"], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
            print("CockroachDB real-server verification passed:", label, flush=True)
        finally:
            remove_owned(name, nonce)


if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    args = argparse.ArgumentParser()
    args.add_argument("--hold", action="store_true")
    try:
        verify(args.parse_args().hold)
    except KeyboardInterrupt:
        raise SystemExit(130)
