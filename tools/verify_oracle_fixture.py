#!/usr/bin/env python3
"""Verify Oracle with a disposable loopback container and remove owned resources."""
import argparse
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import time


def verify(image, output, race=False):
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    output.chmod(0o700)
    name = "nyadb-oracle-" + secrets.token_hex(6)
    private = output / ("private-" + secrets.token_hex(6))
    private.mkdir(mode=0o700)
    password = "NyaDB_" + secrets.token_hex(8) + "!"  # Oracle <= 30 bytes
    envfile = private / "oracle.env"
    envfile.write_text("ORACLE_PASSWORD=" + password + "\nAPP_USER=nyadb_it\nAPP_USER_PASSWORD=" + password + "\n")
    envfile.chmod(0o600)
    owned = False
    report = {"image": image, "race": race, "loopback": True, "checks": []}
    try:
        subprocess.run(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=oracle", "--env-file", str(envfile), "-p", "127.0.0.1::1521", image], check=True, stdout=subprocess.DEVNULL)
        owned = True
        deadline = time.monotonic() + 240
        while time.monotonic() < deadline:
            state = subprocess.check_output(["docker", "inspect", "--format", "{{.State.Running}}", name], text=True).strip()
            if state != "true":
                raise RuntimeError("Oracle fixture exited during startup")
            logs = subprocess.check_output(["docker", "logs", name], text=True, stderr=subprocess.STDOUT)
            if "DATABASE IS READY TO USE!" in logs:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Oracle fixture readiness deadline exceeded")
        port = subprocess.check_output(["docker", "port", name, "1521/tcp"], text=True).strip().rsplit(":", 1)[1]
        env = os.environ.copy()
        env.update(GOWORK="off", CGO_ENABLED="1" if race else "0", NYADB_ORACLE_IT="1", NYADB_ORACLE_PORT=port, NYADB_ORACLE_PASSWORD=password, NYADB_ORACLE_VISUAL_DIR=str(output / "screenshots"))
        for package, pattern, logname in [
            ("./internal/services/oracle", "TestOracle.*", "adapter.log"),
            ("./internal/services/dbruntime", "TestOracleRuntimeDisposableFixture", "runtime.log"),
            ("./internal/ui/shell", "TestOracleSessionAndSharedGridDisposableFixture", "ui.log"),
        ]:
            print("Verifying Oracle", package, flush=True)
            command = ["go", "test", package, "-run", pattern, "-count=1", "-v", "-timeout", "180s"]
            if race:
                command.append("-race")
            with open(output / logname, "w") as log:
                result = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT)
            logpath = output / logname
            text = logpath.read_text().replace(password, "[hidden]")
            logpath.write_text(text)
            print(text, end="", flush=True)
            report["checks"].append({"package": package, "exit": result.returncode})
            if result.returncode:
                raise RuntimeError("Oracle fixture verification failed")
        # Verify LOB editing both without and with the optional package grant.
        sql = "WHENEVER SQLERROR EXIT FAILURE\nALTER SESSION SET CONTAINER=XEPDB1;\nGRANT EXECUTE ON DBMS_CRYPTO TO nyadb_it;\nEXIT\n"
        subprocess.run(["docker", "exec", "-i", name, "sqlplus", "-s", "/", "as", "sysdba"], input=sql, text=True, check=True, stdout=subprocess.DEVNULL)
        command = ["go", "test", "./internal/services/oracle", "-run", "TestOracleLOBFixture", "-count=1", "-v", "-timeout", "180s"]
        if race:
            command.append("-race")
        with open(output / "lob-edit.log", "w") as log:
            result = subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT)
        logpath = output / "lob-edit.log"
        text = logpath.read_text().replace(password, "[hidden]")
        logpath.write_text(text)
        print(text, end="", flush=True)
        report["checks"].append({"feature": "LOB editing with DBMS_CRYPTO", "exit": result.returncode})
        if result.returncode:
            raise RuntimeError("Oracle LOB verification failed")
    finally:
        if owned:
            result = subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            report["owned_container_removed"] = result.returncode == 0
        shutil.rmtree(private)
        report["private_credentials_removed"] = not private.exists()
        (output / "verification.json").write_text(json.dumps(report, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="gvenzl/oracle-xe:21-slim-faststart")
    parser.add_argument("--output", default="build/oracle-fixture")
    parser.add_argument("--race", action="store_true")
    args = parser.parse_args()
    signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    verify(args.image, Path(args.output), args.race)


if __name__ == "__main__":
    main()
