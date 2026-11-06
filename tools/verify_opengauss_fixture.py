#!/usr/bin/env python3
"""Disposable owned openGauss 6 fixture; credentials remain in a private folder."""
import argparse
import io
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import tarfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = "enmotech/opengauss:6.0.0"


def failure_logs(name, password, administrator_password=""):
    result = subprocess.run(["docker", "logs", "--tail", "100", name], capture_output=True, text=True, check=False)
    logs = result.stdout + result.stderr
    # gaussdb switches to its own log after early startup. Read archive members
    # without extracting untrusted paths or printing the container environment.
    for path in ["/home/omm/pg_log", "/var/lib/opengauss/data/pg_log"]:
        archive = subprocess.run(["docker", "cp", name + ":" + path, "-"], capture_output=True, check=False)
        if archive.returncode == 0:
            with tarfile.open(fileobj=io.BytesIO(archive.stdout)) as files:
                for member in files:
                    if member.isfile() and member.name.endswith(".log") and member.size <= 2 * 1024 * 1024:
                        text = files.extractfile(member).read().decode("utf-8", errors="replace")
                        logs += "\n" + member.name + "\n" + "\n".join(text.splitlines()[-80:])
    for secret in [password, administrator_password]:
        if secret:
            logs = logs.replace(secret, "[redacted]")
    return logs


def fixture_sql(command, statement):
    try:
        return subprocess.run(command, input=statement, capture_output=True, text=True, check=False, timeout=10)
    except subprocess.TimeoutExpired:
        return subprocess.CompletedProcess(command, 124, "", "fixture SQL timed out")


def remove_owned(name, nonce):
    inspected = subprocess.run(["docker", "container", "inspect", name], capture_output=True, text=True, check=False)
    if inspected.returncode:
        return
    try:
        value = json.loads(inspected.stdout)[0]
        labels = value.get("Config", {}).get("Labels", {}) or {}
        if labels.get("nyadb.fixture") != "opengauss" or labels.get("nyadb.fixture.nonce") != nonce:
            return
        identifier = value["Id"]
        if not isinstance(identifier, str) or not identifier:
            return
    except (ValueError, KeyError, IndexError, TypeError, AttributeError):
        return
    subprocess.run(["docker", "rm", "-f", identifier], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)


def verify(hold=False, plain=False):
    nonce = uuid.uuid4().hex[:12]
    name = "nyadb-opengauss-it-" + nonce
    base = ROOT / "temp" / "opengauss"
    base.mkdir(parents=True, exist_ok=True)
    output = ROOT / "build" / "opengauss"
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        # openGauss enforces a 32-character maximum for role passwords.
        password = "Aa9!" + secrets.token_hex(12)
        administrator_password = "Bb8!" + secrets.token_hex(12)
        certificates = folder / "certs"
        certificates.mkdir(mode=0o755)
        cert, key = certificates / "server.crt", certificates / "server.key"
        if not plain:
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-noenc", "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1", "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True)
            key.chmod(0o600)
            cert.chmod(0o600)
        envfile = folder / "container.env"
        envfile.write_text("GS_PASSWORD=" + password + "\nGS_DB=postgres\nGS_HOST_AUTH_METHOD=sha256\nOTHER_PG_CONF=shared_buffers=128MB\\nmax_process_memory=2GB\\ncstore_buffers=16MB\\nwal_buffers=16MB\\nwork_mem=4MB\\npassword_encryption_type=2\n")
        if not plain:
            contents = envfile.read_text().rstrip("\n")
            envfile.write_text(contents + "\\nssl=on\\nssl_cert_file='/nyadb-fixture-tls/server.crt'\\nssl_key_file='/nyadb-fixture-tls/server.key'\n")
        envfile.chmod(0o600)
        try:
            tls_mount = ["--mount", "type=bind,source=" + str(certificates) + ",target=/nyadb-fixture-certs,readonly", "--entrypoint", "/bin/sh"]
            # Copy only TLS files into the owned container, where root can give
            # them to omm. Host users do not need chown privileges.
            tls_startup = "mkdir -p /nyadb-fixture-tls && cp /nyadb-fixture-certs/server.crt /nyadb-fixture-certs/server.key /nyadb-fixture-tls/ && chown -R omm:omm /nyadb-fixture-tls && chmod 700 /nyadb-fixture-tls && chmod 600 /nyadb-fixture-tls/* && exec /usr/local/bin/entrypoint.sh gaussdb"
            subprocess.run(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=opengauss", "--label", "nyadb.fixture.nonce=" + nonce, "--memory=4g", "--shm-size=512m", "--cap-add=SYS_NICE", "-p", "127.0.0.1::5432", "--env-file", str(envfile)] + ([] if plain else tls_mount) + [IMAGE] + ([] if plain else ["-c", tls_startup]), check=True, stdout=subprocess.DEVNULL)
            sql = ["docker", "exec", "-i", "--user", "omm", "-e", "LD_LIBRARY_PATH=/usr/local/opengauss/lib", name, "/usr/local/opengauss/bin/gsql", "-d", "postgres", "-v", "ON_ERROR_STOP=1"]
            deadline = time.monotonic() + 120
            administrator_ready = False
            while True:
                process = subprocess.run(["docker", "exec", name, "cat", "/proc/1/comm"], capture_output=True, text=True, check=False)
                if process.stdout.strip() == "gaussdb" and not administrator_ready:
                    # gs_initdb's initial administrator password must be changed
                    # before any other SQL is allowed by this server release.
                    changed = fixture_sql(sql, "ALTER ROLE omm PASSWORD '" + administrator_password + "';\n")
                    administrator_ready = changed.returncode == 0
                probe = fixture_sql(sql, "SELECT 1;\n")
                if probe.returncode == 0 and process.stdout.strip() == "gaussdb":
                    break
                state = json.loads(subprocess.check_output(["docker", "container", "inspect", name], text=True))[0]["State"]
                if not state["Running"]:
                    raise RuntimeError("openGauss container exited before bootstrap:\n" + failure_logs(name, password, administrator_password))
                if time.monotonic() > deadline:
                    raise RuntimeError("openGauss fixture bootstrap timed out:\n" + failure_logs(name, password, administrator_password))
                time.sleep(1)
            bootstrap = "CREATE USER nyadb_it WITH PASSWORD '" + password + "'; CREATE DATABASE nyadb_opengauss_it OWNER nyadb_it;\n"
            bootstrap += "\\connect nyadb_opengauss_it\nCREATE TABLE public.records(id BIGINT PRIMARY KEY,note TEXT,amount NUMERIC(40,20)); INSERT INTO public.records VALUES(1,'alpha',9007199254740993.12345678901234567890),(2,'beta',2); GRANT ALL ON TABLE public.records TO nyadb_it; CREATE SEQUENCE public.fixture_sequence START 987; GRANT SELECT,USAGE ON SEQUENCE public.fixture_sequence TO nyadb_it; CREATE SEQUENCE public.private_sequence START 321;\n"
            result = fixture_sql(sql, bootstrap)
            if result.returncode:
                raise RuntimeError("openGauss fixture account bootstrap failed: " + result.stderr.replace(password, "[redacted]"))
            environment = {
                "NYADB_OPENGAUSS_IT": "1", "NYADB_OPENGAUSS_PORT": subprocess.check_output(["docker", "port", name, "5432/tcp"], text=True).strip().rsplit(":", 1)[1],
                "NYADB_OPENGAUSS_PASSWORD": password, "NYADB_OPENGAUSS_CONTAINER": name, "NYADB_OPENGAUSS_VISUAL_DIR": str(output),
            }
            if not plain:
                environment["NYADB_OPENGAUSS_CA_FILE"] = str(cert)
            config = folder / "environment.json"
            config.write_text(json.dumps(environment))
            config.chmod(0o600)
            print("openGauss fixture ready; private environment file:", config, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_OPENGAUSS_RACE") == "1"
            environment = dict(os.environ, **environment, CGO_ENABLED="1" if race else "0", GOWORK="off")
            with (output / ("race-verification.log" if race else "plain-verification.log" if plain else "tls-verification.log")).open("w") as log:
                subprocess.run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/engines", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^TestOpenGauss", "-count=1", "-v", "-timeout=240s"], cwd=ROOT, env=environment, stdout=log, stderr=subprocess.STDOUT, check=True)
            print("openGauss fixture verification passed", flush=True)
        finally:
            remove_owned(name, nonce)


if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    arguments = argparse.ArgumentParser()
    arguments.add_argument("--hold", action="store_true")
    arguments.add_argument("--plain", action="store_true")
    try:
        args = arguments.parse_args()
        verify(args.hold, args.plain)
    except KeyboardInterrupt:
        raise SystemExit(130)
