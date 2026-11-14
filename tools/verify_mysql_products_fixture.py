#!/usr/bin/env python3
"""Verify MySQL-compatible products against owned disposable Docker servers.

TDSQL and PolarDB use MySQL wire fixtures, not managed cloud instances.
GreatSQL and Dolt use their real product images. Credentials and data are
temporary; only loopback ports are published and only this run's containers
are removed. Logs and captures stay under ignored build/.
"""

import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parent.parent
IMAGES = {
    "mysql": "mysql:8.0",
    "greatsql": "greatsql/greatsql:8.0.32-27",
    "dolt": "dolthub/dolt-sql-server@sha256:aec688ee83e221849479d4250aa6f4acba8874fba79a00bc1f4594b49a5eb240",
}


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)


def inspect(name):
    return json.loads(run(["docker", "inspect", name], capture_output=True).stdout)[0]


def verify(products):
    nonce = uuid.uuid4().hex
    created = []
    output = ROOT / "build" / "mysql-products"
    output.mkdir(parents=True, exist_ok=True)
    base = ROOT / "temp" / "mysql-products"
    base.mkdir(parents=True, exist_ok=True)
    password = secrets.token_hex(24)
    try:
        with tempfile.TemporaryDirectory(prefix="fixture-", dir=base) as tmp:
            folder = Path(tmp)
            for product in products:
                image = IMAGES[product]
                (output / product).mkdir(parents=True, exist_ok=True)
                env_file = folder / (product + ".env")
                env_file.write_text(
                    "MYSQL_ROOT_PASSWORD=" + password + "\n"
                    "MYSQL_ROOT_HOST=%\nMYSQL_DATABASE=nyadb_products_it\n"
                    "DOLT_ROOT_PASSWORD=" + password + "\nDOLT_ROOT_HOST=%\n"
                    "DOLT_DATABASE=nyadb_products_it\n"
                )
                env_file.chmod(0o600)
                name = "nyadb-mysql-products-" + product + "-" + nonce[:12]
                run([
                    "docker", "run", "-d", "--name", name,
                    "--label", "nyadb.fixture=mysql-products",
                    "--label", "nyadb.fixture.nonce=" + nonce,
                    "--env-file", str(env_file), "--memory", "2g",
                    "-p", "127.0.0.1::3306", image,
                ], stdout=subprocess.DEVNULL)
                created.append(name)
                port = run(["docker", "port", name, "3306/tcp"], capture_output=True).stdout.strip().rsplit(":", 1)[1]
                deadline = time.monotonic() + 150
                while True:
                    state = inspect(name)["State"]
                    if not state["Running"]:
                        raise RuntimeError(product + " fixture stopped before readiness")
                    if product == "dolt":
                        ready = subprocess.run(
                            ["docker", "exec", name, "dolt", "sql", "-q", "SELECT 1"],
                            text=True, capture_output=True,
                        ).returncode == 0
                    elif product == "mysql":
                        ready = subprocess.run(
                            ["docker", "exec", name, "mysqladmin", "ping", "--silent"],
                            text=True, capture_output=True,
                        ).returncode == 0
                    else:
                        # GreatSQL's minimal image omits mysqladmin. Its init
                        # marker and an actual TCP protocol greeting prove the
                        # final server is accepting connections.
                        ready = True
                    logs = run(["docker", "logs", name], capture_output=True)
                    startup = logs.stdout + logs.stderr
                    if ready and "init process done" in startup.lower():
                        try:
                            with socket.create_connection(("127.0.0.1", int(port)), timeout=1) as connection:
                                if connection.recv(1):
                                    break
                        except OSError:
                            pass
                    if time.monotonic() >= deadline:
                        raise RuntimeError(product + " fixture readiness timed out")
                    time.sleep(1)
                env = os.environ.copy()
                env.update(
                    CGO_ENABLED="0", GOWORK="off", NYADB_MYSQL_PRODUCTS_IT="1",
                    NYADB_MYSQL_PRODUCTS_PORT=port,
                    NYADB_MYSQL_PRODUCTS_PASSWORD=password,
                    NYADB_MYSQL_PRODUCTS_KINDS="tdsql,polardb" if product == "mysql" else product,
                    NYADB_MYSQL_PRODUCTS_UI_KIND="polardb" if product == "mysql" else product,
                    NYADB_MYSQL_PRODUCTS_VISUAL_DIR=str(output / product),
                )
                print(product + " disposable fixture ready", flush=True)
                with (output / (product + "-verification.log")).open("w") as log:
                    for package in ["./internal/services/dbruntime", "./internal/ui/shell"]:
                        run([
                            "go", "test", package,
                            "-run", "^TestMySQLProducts.*DisposableFixture$",
                            "-count=1", "-timeout", "180s", "-v",
                        ], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
                print(product + " runtime and workbench verification passed", flush=True)
    finally:
        for name in reversed(created):
            try:
                item = inspect(name)
                labels = item["Config"].get("Labels") or {}
                if labels.get("nyadb.fixture.nonce") == nonce and labels.get("nyadb.fixture") == "mysql-products":
                    subprocess.run(["docker", "rm", "-f", "-v", item["Id"]], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)
            except (subprocess.CalledProcessError, KeyError, ValueError):
                pass


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("products", nargs="*", choices=list(IMAGES), default=list(IMAGES))
    args = parser.parse_args()
    verify(args.products or list(IMAGES))
