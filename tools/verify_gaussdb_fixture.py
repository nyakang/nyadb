#!/usr/bin/env python3
"""Owned native GaussDB fixture; this does not substitute openGauss for GaussDB."""
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
IMAGE = "apecloud/gaussdb@sha256:0e997ccb1efde0e7dadde899c1b4aec6361b5b1e262a89f9abadb36fa8643540"


def remove_owned(name, nonce):
    result = subprocess.run(["docker", "inspect", name], capture_output=True, text=True, check=False)
    if result.returncode:
        return
    try:
        value = json.loads(result.stdout)[0]
        labels = value.get("Config", {}).get("Labels", {}) or {}
        if labels.get("nyadb.fixture") != "gaussdb" or labels.get("nyadb.fixture.nonce") != nonce:
            return
        identifier = value["Id"]
        if not isinstance(identifier, str) or not identifier:
            return
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return
    subprocess.run(["docker", "rm", "-f", identifier], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify(hold=False, plain=False):
    nonce = uuid.uuid4().hex[:12]
    name = "nyadb-gaussdb-it-" + nonce
    base = ROOT / "temp" / "gaussdb"
    base.mkdir(parents=True, exist_ok=True)
    output = ROOT / "build" / "gaussdb"
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        password = "Aa9!" + secrets.token_hex(12)
        administrator_password = "Bb8!" + secrets.token_hex(12)
        (folder / "password").write_text(password + "\n")
        (folder / "password").chmod(0o600)
        cert, key = folder / "server.crt", folder / "server.key"
        if not plain:
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
            cert.chmod(0o600)
            key.chmod(0o600)
        config = "listen_addresses='*'\nport=5432\nshared_buffers=128MB\nmax_process_memory=2GB\ncstore_buffers=16MB\nwal_buffers=16MB\nwork_mem=4MB\npassword_encryption_type=2\nlogging_collector=off\n"
        if not plain:
            config += "ssl=on\nssl_cert_file='/nyadb-fixture/server.crt'\nssl_key_file='/nyadb-fixture/server.key'\n"
        (folder / "postgresql.extra.conf").write_text(config)
        (folder / "postgresql.extra.conf").chmod(0o600)
        startup = """set -eu
mkdir -p /nyadb-fixture /nyadb-data
cp /nyadb-source/password /nyadb-source/postgresql.extra.conf /nyadb-fixture/
if test -f /nyadb-source/server.crt; then cp /nyadb-source/server.crt /nyadb-source/server.key /nyadb-fixture/; fi
chown -R omm:omm /nyadb-fixture /nyadb-data
chmod 700 /nyadb-fixture /nyadb-data
chmod 600 /nyadb-fixture/*
/opt/gaussdb/app/bin/gosu omm /opt/gaussdb/app/bin/gs_initdb -D /nyadb-data --nodename=nyadb_dn --dbcompatibility=PG --encoding=UTF8 --pwfile=/nyadb-fixture/password
cat /nyadb-fixture/postgresql.extra.conf >> /nyadb-data/postgresql.conf
printf '\\nlocal all all trust\\nhost all all 0.0.0.0/0 sha256\\n' > /nyadb-data/pg_hba.conf
chown omm:omm /nyadb-data/postgresql.conf /nyadb-data/pg_hba.conf
chmod 600 /nyadb-data/postgresql.conf /nyadb-data/pg_hba.conf
exec /opt/gaussdb/app/bin/gosu omm /opt/gaussdb/app/bin/gaussdb --single_node -D /nyadb-data
"""
        try:
            subprocess.run(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=gaussdb", "--label", "nyadb.fixture.nonce=" + nonce, "--memory=4g", "--shm-size=512m", "--cap-add=SYS_NICE", "-p", "127.0.0.1::5432", "-e", "LD_LIBRARY_PATH=/opt/gaussdb/app/lib", "--mount", "type=bind,source=" + str(folder) + ",target=/nyadb-source,readonly", "--entrypoint", "/bin/bash", IMAGE, "-c", startup], check=True, stdout=subprocess.DEVNULL)
            command = ["docker", "exec", "-i", "--user", "omm", name, "/opt/gaussdb/app/bin/gsql", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-t", "-A"]
            deadline = time.monotonic() + 120
            while True:
                try:
                    probe = subprocess.run(command, input="ALTER ROLE omm PASSWORD '" + administrator_password + "';\nSELECT version();\n", capture_output=True, text=True, check=False, timeout=10)
                except subprocess.TimeoutExpired:
                    probe = subprocess.CompletedProcess(command, 124, "", "fixture SQL timed out")
                if probe.returncode == 0:
                    if "GaussDB Kernel" not in probe.stdout or "openGauss" in probe.stdout:
                        raise RuntimeError("candidate image does not prove native GaussDB identity")
                    (output / "server-version.txt").write_text(probe.stdout.replace(administrator_password, "[redacted]"))
                    break
                state = json.loads(subprocess.check_output(["docker", "inspect", name], text=True))[0]["State"]
                if not state["Running"] or time.monotonic() > deadline:
                    logs = subprocess.run(["docker", "logs", "--tail", "100", name], capture_output=True, text=True, check=False)
                    detail = (logs.stdout + logs.stderr).replace(password, "[redacted]").replace(administrator_password, "[redacted]")
                    raise RuntimeError("GaussDB fixture bootstrap failed:\n" + detail)
                time.sleep(1)
            bootstrap = "CREATE USER nyadb_it WITH PASSWORD '" + password + "'; CREATE DATABASE nyadb_gaussdb_it OWNER nyadb_it;\n"
            result = subprocess.run(command, input=bootstrap, capture_output=True, text=True, check=False, timeout=15)
            if result.returncode:
                raise RuntimeError("GaussDB fixture user bootstrap failed: " + result.stderr.replace(password, "[redacted]"))
            environment = {"NYADB_GAUSSDB_IT": "1", "NYADB_GAUSSDB_PORT": subprocess.check_output(["docker", "port", name, "5432/tcp"], text=True).strip().rsplit(":", 1)[1], "NYADB_GAUSSDB_PASSWORD": password, "NYADB_GAUSSDB_CONTAINER": name}
            if not plain:
                environment["NYADB_GAUSSDB_CA_FILE"] = str(cert)
            private = folder / "environment.json"
            private.write_text(json.dumps(environment))
            private.chmod(0o600)
            print("GaussDB fixture ready; private environment file:", private, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_GAUSSDB_RACE") == "1"
            environment = dict(os.environ, **environment, CGO_ENABLED="1" if race else "0", GOWORK="off")
            logpath = output / ("race-transport.log" if race else "plain-transport.log" if plain else "tls-transport.log")
            with logpath.open("w") as log:
                subprocess.run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/gaussdb", "-run", "^TestNativeGaussDB", "-count=1", "-v", "-timeout=120s"], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
            print("GaussDB native transport verification passed:", logpath, flush=True)
        finally:
            remove_owned(name, nonce)


if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser()
    parser.add_argument("--hold", action="store_true")
    parser.add_argument("--plain", action="store_true")
    args = parser.parse_args()
    try:
        verify(args.hold, args.plain)
    except KeyboardInterrupt:
        raise SystemExit(130)
