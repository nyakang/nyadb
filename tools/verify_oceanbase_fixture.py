#!/usr/bin/env python3
"""Disposable, owned OceanBase 4.3.5 LTS MySQL tenant on a loopback port."""
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
IMAGE = "oceanbase/oceanbase-ce:4.3.5.6-106000012026040916"


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def remove_owned(resource, name, nonce):
    result = subprocess.run(["docker", resource, "inspect", name], capture_output=True, text=True, check=False)
    if result.returncode:
        return
    try:
        item = json.loads(result.stdout)[0]
        labels = (item.get("Config", {}).get("Labels", {}) if resource == "container" else item.get("Labels", {})) or {}
        if labels.get("nyadb.fixture.nonce") != nonce or labels.get("nyadb.fixture") != "oceanbase":
            return
        identifier = item["Id"]
    except (ValueError, KeyError, IndexError, TypeError):
        return
    cmd = ["docker", "rm", "-f", identifier] if resource == "container" else ["docker", "network", "rm", identifier]
    subprocess.run(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify(hold=False):
    nonce = uuid.uuid4().hex[:12]
    name = "nyadb-oceanbase-it-" + nonce
    base = ROOT / "temp" / "oceanbase"
    base.mkdir(parents=True, exist_ok=True)
    out = ROOT / "build" / "oceanbase"
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        password = secrets.token_hex(24)
        root_password = secrets.token_hex(24)
        (folder / "init").mkdir(mode=0o700)
        (folder / "init" / "init.sql").write_text(
            "CREATE DATABASE nyadb_oceanbase_it CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;\n"
            "CREATE USER 'nyadb_ob'@'%' IDENTIFIED BY '" + password + "';\n"
            "GRANT ALL PRIVILEGES ON *.* TO 'nyadb_ob'@'%' WITH GRANT OPTION;\n"
            "CREATE TABLE nyadb_oceanbase_it.records (id BIGINT PRIMARY KEY, note VARCHAR(80), amount DECIMAL(40,20));\n"
            "CREATE SEQUENCE nyadb_oceanbase_it.fixture_sequence START WITH 987;\n"
            "INSERT INTO nyadb_oceanbase_it.records VALUES (1,'alpha',9007199254740993.12345678901234567890),(2,'beta',2);\n"
        )
        (folder / "init" / "init.sql").chmod(0o600)
        (folder / "docker.env").write_text("OB_TENANT_PASSWORD="+root_password+"\nOB_MEMORY_LIMIT=6G\nOB_DATAFILE_SIZE=5G\nOB_LOG_DISK_SIZE=5G\n")
        (folder / "docker.env").chmod(0o600)
        (folder / "client.cnf").write_text("[client]\nuser=root@test\npassword="+root_password+"\nhost=127.1\nport=2881\n")
        (folder / "client.cnf").chmod(0o600)
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(folder / "server-key.pem"), "-out", str(folder / "ca.pem"), "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost", "-addext", "basicConstraints=critical,CA:TRUE"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (folder / "server-key.pem").chmod(0o600)
        try:
            run(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=oceanbase", "--label", "nyadb.fixture.nonce="+nonce, "--memory", "12g", "--ulimit", "nofile=65536:65536", "--env-file", str(folder / "docker.env"), "-p", "127.0.0.1::2881", "-v", str(folder)+":/fixture:ro", "-v", str(folder / "init")+":/root/boot/init.d:ro", IMAGE], stdout=subprocess.DEVNULL)
            port = run(["docker", "port", name, "2881/tcp"], capture_output=True).stdout.strip().rsplit(":", 1)[1]
            deadline = time.monotonic() + 420
            while True:
                result = run(["docker", "logs", name], capture_output=True)
                state = json.loads(run(["docker", "inspect", name], capture_output=True).stdout)[0]["State"]
                if not state["Running"]:
                    raise RuntimeError("OceanBase container exited before bootstrap")
                if "boot success!" in result.stdout:
                    break
                if time.monotonic() > deadline:
                    raise RuntimeError("OceanBase bootstrap timed out")
                time.sleep(1)
            run(["docker", "exec", name, "mkdir", "-p", "/root/ob/observer/wallet"])
            for source, target in [("ca.pem", "ca.pem"), ("ca.pem", "server-cert.pem"), ("server-key.pem", "server-key.pem")]:
                run(["docker", "exec", name, "cp", "/fixture/"+source, "/root/ob/observer/wallet/"+target])
            run(["docker", "exec", name, "obclient", "-h127.1", "-uroot@sys", "-P2881", "-e", "ALTER SYSTEM SET ssl_client_authentication=TRUE"])
            run(["docker", "exec", name, "obclient", "--defaults-extra-file=/fixture/client.cnf", "-e", "ALTER USER 'nyadb_ob'@'%' REQUIRE SSL"])
            env = os.environ.copy()
            env.update(CGO_ENABLED="0", GOWORK="off", NYADB_OCEANBASE_IT="1", NYADB_OCEANBASE_PORT=port, NYADB_OCEANBASE_PASSWORD=password, NYADB_OCEANBASE_ROOT_PASSWORD=root_password, NYADB_OCEANBASE_CA_FILE=str(folder / "ca.pem"), NYADB_OCEANBASE_CONTAINER=name, NYADB_OCEANBASE_VISUAL_DIR=str(out))
            path = folder / "environment.json"
            path.write_text(json.dumps({k:v for k,v in env.items() if k.startswith("NYADB_OCEANBASE_")}))
            path.chmod(0o600)
            print("OceanBase fixture ready; private environment file:", path, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_OCEANBASE_RACE") == "1"
            if race:
                env["CGO_ENABLED"] = "1"
            label = "race" if race else "tcp-tls"
            with (out / f"{label}-verification.log").open("w") as log:
                run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/mysql", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^TestOceanBase", "-count=1", "-timeout", "240s", "-v"], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
            print("OceanBase real-server verification passed:", label, flush=True)
        finally:
            remove_owned("container", name, nonce)


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
