#!/usr/bin/env python3
"""Owned native KingbaseES V8R6 PG-mode transport and workbench fixture."""
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
IMAGE = "lihongjie0209/kingbase@sha256:9f713f8ec943be81bd4dbb321e2158b0ba24e67cd95f2c2864225c37c2ce8d9e"
BIN = "/home/kingbase/install/kingbase/bin"


def remove_owned(name, nonce):
    result = subprocess.run(["docker", "inspect", name], capture_output=True, text=True, check=False)
    if result.returncode:
        return
    try:
        value = json.loads(result.stdout)[0]
        labels = value.get("Config", {}).get("Labels", {}) or {}
        if labels.get("nyadb.fixture") != "kingbase" or labels.get("nyadb.fixture.nonce") != nonce:
            return
        identifier = value["Id"]
        if not isinstance(identifier, str) or not identifier:
            return
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return
    subprocess.run(["docker", "rm", "-f", identifier], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify(hold=False, plain=False):
    nonce = uuid.uuid4().hex[:12]
    name = "nyadb-kingbase-it-" + nonce
    base = ROOT / "temp" / "kingbase"
    base.mkdir(parents=True, exist_ok=True)
    output = ROOT / "build" / "kingbase"
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        password = "Aa9!" + secrets.token_hex(12)
        administrator_password = "Bb8!" + secrets.token_hex(12)
        (folder / "administrator-password").write_text(administrator_password + "\n")
        (folder / "administrator-password").chmod(0o600)
        cert, key = folder / "server.crt", folder / "server.key"
        if not plain:
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
            cert.chmod(0o600)
            key.chmod(0o600)
        config = "listen_addresses='*'\nport=54321\nmax_connections=10\nshared_buffers=128MB\nlogging_collector=off\nlog_statement='none'\nlog_min_error_statement='panic'\npassword_encryption='scram-sha-256'\n"
        if not plain:
            config += "ssl=on\nssl_cert_file='/nyadb-fixture/server.crt'\nssl_key_file='/nyadb-fixture/server.key'\n"
        (folder / "kingbase.extra.conf").write_text(config)
        (folder / "kingbase.extra.conf").chmod(0o600)
        startup = """set -eu
export LD_LIBRARY_PATH=/home/kingbase/install/kingbase/lib
mkdir -p /nyadb-fixture /nyadb-data
cp /nyadb-source/administrator-password /nyadb-source/kingbase.extra.conf /nyadb-fixture/
if test -f /nyadb-source/server.crt; then cp /nyadb-source/server.crt /nyadb-source/server.key /nyadb-fixture/; fi
chown -R kingbase:kingbase /nyadb-fixture /nyadb-data
chmod 700 /nyadb-fixture /nyadb-data
chmod 600 /nyadb-fixture/*
cd /home/kingbase
setpriv --reuid=1000 --regid=1000 --init-groups /home/kingbase/install/kingbase/bin/initdb -D /nyadb-data -U nyadb_admin --pwfile=/nyadb-fixture/administrator-password --dbmode=pg --encoding=UTF8 --locale=C --auth-local=trust --auth-host=scram-sha-256
cat /nyadb-fixture/kingbase.extra.conf >> /nyadb-data/kingbase.conf
printf '\\nlocal all all trust\\nhost all all 0.0.0.0/0 scram-sha-256\\n' > /nyadb-data/sys_hba.conf
chown kingbase:kingbase /nyadb-data/kingbase.conf /nyadb-data/sys_hba.conf
chmod 600 /nyadb-data/kingbase.conf /nyadb-data/sys_hba.conf
exec setpriv --reuid=1000 --regid=1000 --init-groups /home/kingbase/install/kingbase/bin/kingbase -D /nyadb-data
"""
        try:
            subprocess.run(["docker", "run", "-d", "--user", "root", "--name", name, "--label", "nyadb.fixture=kingbase", "--label", "nyadb.fixture.nonce=" + nonce, "--memory=2g", "--shm-size=256m", "-p", "127.0.0.1::54321", "--mount", "type=bind,source=" + str(folder) + ",target=/nyadb-source,readonly", "--entrypoint", "/bin/bash", IMAGE, "-c", startup], check=True, stdout=subprocess.DEVNULL)
            command = ["docker", "exec", "-i", "--user", "kingbase", name, BIN + "/ksql", "-U", "nyadb_admin", "-d", "template1", "-v", "ON_ERROR_STOP=1", "-t", "-A"]
            deadline = time.monotonic() + 120
            while True:
                try:
                    probe = subprocess.run(command, input="SELECT version(); SHOW database_mode; SELECT datname FROM pg_database ORDER BY datname;\n", capture_output=True, text=True, check=False, timeout=10)
                except subprocess.TimeoutExpired:
                    probe = subprocess.CompletedProcess(command, 124, "", "fixture SQL timed out")
                if probe.returncode == 0:
                    if "KingbaseES V008R006C009B0014" not in probe.stdout or "\npg\n" not in probe.stdout:
                        raise RuntimeError("candidate image does not prove expected native KingbaseES PG-mode identity")
                    (output / "server-version.txt").write_text(probe.stdout)
                    break
                state = json.loads(subprocess.check_output(["docker", "inspect", name], text=True))[0]["State"]
                if not state["Running"] or time.monotonic() > deadline:
                    logs = subprocess.run(["docker", "logs", "--tail", "100", name], capture_output=True, text=True, check=False)
                    detail = (logs.stdout + logs.stderr).replace(password, "[redacted]").replace(administrator_password, "[redacted]")
                    raise RuntimeError("Kingbase fixture bootstrap failed:\n" + detail)
                time.sleep(1)
            bootstrap = "CREATE USER nyadb_it WITH PASSWORD '" + password + "'; CREATE DATABASE nyadb_kingbase_it OWNER nyadb_it; SELECT rolpassword LIKE 'SCRAM-SHA-256$%' FROM pg_authid WHERE rolname='nyadb_it';\n"
            result = subprocess.run(command, input=bootstrap, capture_output=True, text=True, check=False, timeout=15)
            if result.returncode:
                raise RuntimeError("Kingbase fixture user bootstrap failed: " + result.stderr.replace(password, "[redacted]"))
            if not result.stdout.rstrip().endswith("t"):
                raise RuntimeError("Kingbase fixture did not verify a SCRAM-SHA-256 account")
            environment = {"NYADB_KINGBASE_IT": "1", "NYADB_KINGBASE_PORT": subprocess.check_output(["docker", "port", name, "54321/tcp"], text=True).strip().rsplit(":", 1)[1], "NYADB_KINGBASE_PASSWORD": password, "NYADB_KINGBASE_CONTAINER": name, "NYADB_KINGBASE_VISUAL_DIR": str(output / "ui")}
            if not plain:
                environment["NYADB_KINGBASE_CA_FILE"] = str(cert)
            private = folder / "environment.json"
            private.write_text(json.dumps(environment))
            private.chmod(0o600)
            print("Kingbase fixture ready; private environment file:", private, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_KINGBASE_RACE") == "1"
            environment = dict(os.environ, **environment, CGO_ENABLED="1" if race else "0", GOWORK="off")
            logpath = output / ("race-core.log" if race else "plain-core.log" if plain else "tls-core.log")
            with logpath.open("w") as log:
                subprocess.run(["go", "test"] + (["-race"] if race else []) + ["-p=1", "./internal/services/kingbase", "./internal/services/engines", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^Test(NativeKingbase|Kingbase)", "-count=1", "-v", "-timeout=240s"], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
            print("Kingbase native transport and core verification passed:", logpath, flush=True)
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
