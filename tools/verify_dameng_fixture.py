#!/usr/bin/env python3
"""Owned DM8 deployment and optional driver, native session or workbench audit.

Each audit verifies only its stated scope against the captured native server.
No vendor driver source or binaries are redistributed with NyaDB.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "sizx/dm8@sha256:c4088323442747a45d58bd33ef59e657c9b85f9e7dd956ba60b36d04a35b141d"
VERSION = "08134283904-20220804-166351-20005"


def remove_owned(identifier, nonce):
    result = subprocess.run(["docker", "inspect", identifier], capture_output=True, text=True, check=False)
    if result.returncode:
        return
    try:
        value = json.loads(result.stdout)[0]
        labels = value.get("Config", {}).get("Labels", {}) or {}
        if labels.get("nyadb.fixture") != "dameng" or labels.get("nyadb.fixture.nonce") != nonce:
            return
        captured = value["Id"]
        if captured != identifier or not isinstance(captured, str) or not re.fullmatch(r"[0-9a-f]{64}", captured):
            return
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return
    subprocess.run(["docker", "rm", "-f", captured], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def create_tls_material(folder):
    """Create independent, short-lived CA/server/client keys for this fixture."""
    server = folder / "server_ssl"
    client = folder / "client_ssl"
    for path in (server, client):
        path.mkdir(mode=0o700)
    def openssl(*args):
        subprocess.run(["openssl", *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    openssl("req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(folder / "ca-key.pem"), "-out", str(folder / "ca-cert.pem"), "-days", "1", "-subj", "/CN=NyaDB disposable DM8 CA", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign")
    for path, name, subject, usage, san in ((server, "server", "localhost", "serverAuth", "DNS:localhost,IP:127.0.0.1"), (client, "client", "SYSDBA", "clientAuth", "DNS:SYSDBA")):
        openssl("req", "-new", "-newkey", "rsa:2048", "-nodes", "-keyout", str(path / (name + "-key.pem")), "-out", str(path / (name + ".csr")), "-subj", "/CN=" + subject)
        ext = path / "extensions"
        ext.write_text("basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=" + usage + "\nsubjectAltName=" + san + "\n")
        openssl("x509", "-req", "-in", str(path / (name + ".csr")), "-CA", str(folder / "ca-cert.pem"), "-CAkey", str(folder / "ca-key.pem"), "-CAcreateserial", "-out", str(path / (name + "-cert.pem")), "-days", "1", "-sha256", "-extfile", str(ext))
        (path / "ca-cert.pem").write_bytes((folder / "ca-cert.pem").read_bytes())
    for path in folder.rglob("*"):
        if path.is_file():
            path.chmod(0o600)


def verify(hold=False, audit_driver=False, race=False, audit_dpi=False, dpi_library=None, tls=False, audit_workbench=False):
    nonce = uuid.uuid4().hex
    base = ROOT / "temp" / "dameng"
    base.mkdir(parents=True, exist_ok=True)
    output = ROOT / "build" / "dameng"
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        password = "Nya" + secrets.token_hex(16) + "a9"
        password_file = folder / "admin-password"
        password_file.write_text(password)
        password_file.chmod(0o600)
        if tls:
            create_tls_material(folder)
        startup = """set -eu
export LD_LIBRARY_PATH=/opt/dmdbms/bin
cd /opt/dmdbms/bin
mkdir -p /nyadb-fixture/data
if [ ! -f /nyadb-fixture/data/DAMENG/dm.ini ]; then
./dminit PATH=/nyadb-fixture/data PAGE_SIZE=16 CASE_SENSITIVE=1 UNICODE_FLAG=1 LENGTH_IN_CHAR=1 SYSDBA_PWD="$(cat /nyadb-source/admin-password)" > /nyadb-fixture/init.log 2>&1
fi
if [ -d /nyadb-source/server_ssl ]; then
mkdir -p /opt/dmdbms/bin/server_ssl
cp /nyadb-source/server_ssl/* /opt/dmdbms/bin/server_ssl/
sed -i 's/^[[:space:]]*ENABLE_ENCRYPT[[:space:]]*=.*/ENABLE_ENCRYPT = 1/' /nyadb-fixture/data/DAMENG/dm.ini
fi
exec ./dmserver /nyadb-fixture/data/DAMENG/dm.ini -noconsole
"""
        identifier = None
        try:
            identifier = subprocess.check_output(["docker", "create", "--name", "nyadb-dameng-it-" + nonce[:12], "--label", "nyadb.fixture=dameng", "--label", "nyadb.fixture.nonce=" + nonce, "--memory=2g", "--shm-size=256m", "-p", "127.0.0.1::5236", "--mount", "type=bind,source=" + str(folder) + ",target=/nyadb-source,readonly", "--entrypoint", "/bin/bash", IMAGE, "-c", startup], text=True).strip()
            if not re.fullmatch(r"[0-9a-f]{64}", identifier):
                raise RuntimeError("Docker did not return an immutable container ID")
            subprocess.run(["docker", "start", identifier], check=True, stdout=subprocess.DEVNULL)
            command = ["docker", "exec", "-i", "--env", "LD_LIBRARY_PATH=/opt/dmdbms/bin", identifier, "/opt/dmdbms/bin/disql", "-S", "/nolog"]
            sql = "CONN SYSDBA/\"" + password + "\"@localhost:5236\n" + """SELECT 'NYADB_DM_UTF8=' || SF_GET_UNICODE_FLAG() AS CHECK_VALUE FROM DUAL;
SELECT 'NYADB_DM_CASE=' || SF_GET_CASE_SENSITIVE_FLAG() AS CHECK_VALUE FROM DUAL;
SELECT 'NYADB_DM_MODE=' || PARA_VALUE AS CHECK_VALUE FROM V$DM_INI WHERE PARA_NAME='COMPATIBLE_MODE';
SELECT 'NYADB_DM_DATABASE=' || NAME AS CHECK_VALUE FROM V$DATABASE;
SELECT 'NYADB_DM_TLS=' || PARA_VALUE AS CHECK_VALUE FROM V$DM_INI WHERE PARA_NAME='ENABLE_ENCRYPT';
SELECT BANNER FROM V$VERSION;
EXIT;
"""
            if tls:
                sql = sql.replace("@localhost:5236\n", '@localhost:5236#{ssl_path=/nyadb-source/client_ssl}\n')
            deadline = time.monotonic() + 120
            while True:
                try:
                    probe = subprocess.run(command, input=sql, capture_output=True, text=True, check=False, timeout=10)
                except subprocess.TimeoutExpired:
                    probe = subprocess.CompletedProcess(command, 124, "", "fixture SQL timed out")
                checks = ["NYADB_DM_UTF8=1", "NYADB_DM_CASE=1", "NYADB_DM_MODE=0", "NYADB_DM_DATABASE=DAMENG", "NYADB_DM_TLS=" + ("1" if tls else "0")]
                if probe.returncode == 0 and all(re.search(r"^\s*\d+\s+" + re.escape(check) + r"\s*$", probe.stdout, re.MULTILINE) for check in checks):
                    if not re.search(r"^\s*\d+\s+" + re.escape(VERSION) + r"\s*$", probe.stdout, re.MULTILINE) or not re.search(r"^\s*\d+\s+DM Database Server 64 V8\s*$", probe.stdout, re.MULTILINE):
                        raise RuntimeError("DM8 candidate did not prove the expected native server version")
                    (output / "server-identity.txt").write_text(probe.stdout.replace(password, "[redacted]"))
                    break
                state = json.loads(subprocess.check_output(["docker", "inspect", identifier], text=True))[0]["State"]
                if not state["Running"] or time.monotonic() > deadline:
                    raise RuntimeError("DM8 fixture startup or identity validation failed; see build/dameng/server.log")
                time.sleep(1)
            port = subprocess.check_output(["docker", "port", identifier, "5236/tcp"], text=True).strip().rsplit(":", 1)[1]
            private = folder / "probe.json"
            private.write_text(json.dumps({"endpoint": "127.0.0.1:" + port, "password_file": str(password_file), "output": str(output / "driver-audit.json"), "native_ssl_directory": str(folder / "client_ssl") if tls else ""}))
            private.chmod(0o600)
            print("DM8 fixture verified; private probe configuration:", private, flush=True)
            if audit_driver:
                environment = dict(os.environ, GOWORK="off", CGO_ENABLED="1" if race else "0")
                with (output / ("driver-race.log" if race else "driver-audit.log")).open("w") as log:
                    subprocess.run(["go", "run"] + (["-race"] if race else []) + [".", str(private)], cwd=ROOT / "tools" / "damengprobe", env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
                print("DM8 isolated driver audit recorded; safe_for_workbench remains false", flush=True)
            if audit_dpi:
                environment = dict(os.environ, GOWORK="off", CGO_ENABLED="1" if race else "0", NYADB_DAMENG_DPI_IT="1", NYADB_DAMENG_DPI_CONFIG=str(private), NYADB_DAMENG_DPI_LIBRARY=str(Path(dpi_library).resolve()))
                with (output / ("dpi-race.log" if race else "dpi-audit.log")).open("w") as log:
                    subprocess.run(["go", "test"] + (["-race"] if race else []) + ["-count=1", "-timeout=180s", "-v", "./internal/services/dameng", "./internal/services/dameng/dpi", "./internal/services/dameng/dpiworker"], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
                print("DM8 " + ("verified server TLS" if tls else "loopback") + " DPI verification passed", flush=True)
            if audit_workbench:
                environment = dict(os.environ, GOWORK="off", CGO_ENABLED="1" if race else "0", NYADB_DAMENG_DPI_IT="1", NYADB_DAMENG_DPI_CONFIG=str(private), NYADB_DAMENG_DPI_LIBRARY=str(Path(dpi_library).resolve()), NYADB_DAMENG_VISUAL_DIR=str(output / "ui"))
                name = "workbench-" + ("tls" if tls else "plain") + ("-race" if race else "") + ".log"
                with (output / name).open("w") as log:
                    subprocess.run(["go", "test"] + (["-race"] if race else []) + ["-count=1", "-timeout=5m", "-run=^(TestDameng|TestNativeDamengSQL)", "-v", "./internal/db/sqlexport", "./internal/services/dameng", "./internal/services/dbruntime", "./internal/ui/shell", "./internal/ui/dataworkspace"], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
                print("DM8 native runtime and workbench verification passed", flush=True)
            if hold:
                while True:
                    time.sleep(1)
        finally:
            if identifier:
                logs = subprocess.run(["docker", "logs", identifier], capture_output=True, text=True, check=False)
                (output / "server.log").write_text((logs.stdout + logs.stderr).replace(password, "[redacted]"))
                remove_owned(identifier, nonce)


if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    parser = argparse.ArgumentParser()
    parser.add_argument("--hold", action="store_true")
    parser.add_argument("--audit-driver", action="store_true", help="audit the unmodified external Go driver in its isolated research module")
    parser.add_argument("--audit-dpi", action="store_true", help="verify the native DPI boundary and physical-session product/schema gate against an externally installed client")
    parser.add_argument("--audit-workbench", action="store_true", help="verify native runtime publication, shared-grid editing, SQL copy/export and SQL lifecycle in the workbench")
    parser.add_argument("--dpi-library", type=Path, help="path to the caller's original native client library; no binaries are installed or redistributed")
    parser.add_argument("--race", action="store_true", help="run the enabled audits with the Go race detector")
    parser.add_argument("--tls", action="store_true", help="deploy the owned server with native certificate TLS")
    args = parser.parse_args()
    if args.race and not (args.audit_driver or args.audit_dpi or args.audit_workbench):
        parser.error("--race requires --audit-driver, --audit-dpi or --audit-workbench")
    if args.tls and args.audit_driver:
        parser.error("--tls requires the DPI audit; the external Go-driver audit accepts a plain server only")
    if (args.audit_dpi or args.audit_workbench) and (not args.dpi_library or not args.dpi_library.is_file()):
        parser.error("--audit-dpi or --audit-workbench requires an existing --dpi-library")
    if args.dpi_library and not (args.audit_dpi or args.audit_workbench):
        parser.error("--dpi-library requires --audit-dpi or --audit-workbench")
    try:
        verify(args.hold, args.audit_driver, args.race, args.audit_dpi, args.dpi_library, args.tls, args.audit_workbench)
    except KeyboardInterrupt:
        raise SystemExit(130)
