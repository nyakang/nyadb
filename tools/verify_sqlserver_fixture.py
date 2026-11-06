#!/usr/bin/env python3
"""Run real SQL Server fixtures on loopback and clean only our containers."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import time


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def verify(image, output, race=False):
    root = output.resolve()
    root.mkdir(parents=True, exist_ok=True)
    root.chmod(0o700)
    token = secrets.token_hex(6)
    name = "nyadb-sqlserver-" + token
    password = "NyaDB!" + secrets.token_hex(20)
    private = root / ("fixture-" + token)
    private.mkdir(mode=0o700)
    certs = private / "certs"
    certs.mkdir(mode=0o755)
    envfile = private / "server.env"
    envfile.write_text("ACCEPT_EULA=Y\nMSSQL_PID=Developer\nMSSQL_SA_PASSWORD=" + password + "\nSQLCMDPASSWORD=" + password + "\n")
    envfile.chmod(0o600)
    try:
        with open(root / "certificate-generation.log", "w") as log:
            run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "2", "-keyout", str(private / "ca.key"), "-out", str(certs / "ca.crt"), "-subj", "/CN=NyaDB SQL Server fixture CA"], stdout=log, stderr=log)
            run(["openssl", "req", "-newkey", "rsa:2048", "-nodes", "-keyout", str(certs / "server.key"), "-out", str(private / "server.csr"), "-subj", "/CN=localhost"], stdout=log, stderr=log)
            extensions = private / "extensions.cnf"
            extensions.write_text("subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nkeyUsage=digitalSignature,keyEncipherment\n")
            run(["openssl", "x509", "-req", "-in", str(private / "server.csr"), "-CA", str(certs / "ca.crt"), "-CAkey", str(private / "ca.key"), "-CAcreateserial", "-days", "2", "-extfile", str(extensions), "-out", str(certs / "server.crt")], stdout=log, stderr=log)
        os.chown(certs / "server.key", 10001, 0)
        (certs / "server.key").chmod(0o600)
        config = private / "mssql.conf"
        config.write_text("[network]\ntlscert = /var/opt/mssql/fixture/server.crt\ntlskey = /var/opt/mssql/fixture/server.key\ntlsprotocols = 1.2\nforceencryption = 1\n")
        config.chmod(0o644)
        run(["docker", "run", "-d", "--name", name, "--env-file", str(envfile), "-p", "127.0.0.1::1433", "-v", str(certs) + ":/var/opt/mssql/fixture:ro", "-v", str(config) + ":/var/opt/mssql/mssql.conf:ro", image], stdout=subprocess.DEVNULL)
        deadline = time.monotonic() + 180
        tools = ["/opt/mssql-tools18/bin/sqlcmd", "/opt/mssql-tools/bin/sqlcmd"]
        while time.monotonic() < deadline:
            ready = False
            for tool in tools:
                result = subprocess.run(["docker", "exec", name, tool, "-S", "localhost", "-U", "sa", "-C", "-b", "-Q", "SELECT 1"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                if result.returncode == 0:
                    ready = True
                    break
            if ready:
                break
            time.sleep(1)
        else:
            raise RuntimeError("isolated SQL Server fixture did not become ready")
        port = subprocess.check_output(["docker", "port", name, "1433/tcp"], text=True).strip().rsplit(":", 1)[1]
        env = os.environ.copy()
        env.update(CGO_ENABLED="1" if race else "0", NYADB_SQLSERVER_IT="1", NYADB_SQLSERVER_PORT=port, NYADB_SQLSERVER_PASSWORD=password, NYADB_SQLSERVER_CA_FILE=str(certs / "ca.crt"), NYADB_SQLSERVER_VISUAL_DIR=str(root))
        # Driver diagnostics are redacted by the adapter. No secret is passed
        # in command arguments or retained in the verification report.
        for package, pattern, logname in [
            ("./internal/services/dbruntime", "TestSQLServerWorkbenchDisposableFixture", "runtime.log"),
            ("./internal/ui/shell", "TestSQLServerSessionAndSharedGridDisposableFixture", "ui.log"),
        ]:
            print("Verifying", image, package, flush=True)
            with open(root / logname, "w") as log:
                command = ["go", "test", package, "-run", pattern, "-count=1", "-v"]
                if race:
                    command.append("-race")
                result = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT)
            print((root / logname).read_text(), end="", flush=True)
            if result.returncode:
                raise RuntimeError("SQL Server fixture verification failed")
        (root / "verification.json").write_text(json.dumps({"image": image, "tls": "verify-full", "race":race, "fixture": "loopback, unique container, disposable", "validated": ["runtime", "shared grid UI"]}, indent=2) + "\n")
    finally:
        subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        # Delete generated passwords and private keys, retaining public CA and
        # logs/screenshots only. This directory contains only our fixture.
        import shutil
        shutil.rmtree(private)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="mcr.microsoft.com/mssql/server:2022-latest")
    parser.add_argument("--output", default="build/sqlserver-2022")
    parser.add_argument("--race", action="store_true")
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    verify(args.image, Path(args.output), args.race)


if __name__ == "__main__":
    main()
