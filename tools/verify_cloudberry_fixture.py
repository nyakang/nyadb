#!/usr/bin/env python3
"""Verify Cloudberry against an owned single-node sandbox and remove it.

The default image is built from Apache's licensed sandbox Dockerfile; source
and provenance stay in ignored temp/cloudberry-fixture. No host data mounts,
fixed container names, SSH publication or fixed host ports are used.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import time


ROOT = Path(__file__).resolve().parents[1]


def command(args: list[str], *, sql: str | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, input=sql, text=True, capture_output=True, check=check, cwd=ROOT)


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="nyadb-cloudberry-fixture:2.1.0")
    parser.add_argument("--race", action="store_true", help="enable Go's race detector")
    args = parser.parse_args()
    command(["docker", "image", "inspect", args.image])
    owner = secrets.token_hex(6)
    name = f"nyadb-cloudberry-{owner}"
    output = ROOT / "build/mysql-products"
    output.mkdir(parents=True, exist_ok=True)
    started = False
    try:
        command([
            "docker", "run", "-dit", "--name", name, "--hostname", "cdw",
            "--label", "nyadb.fixture=cloudberry", "--label", f"nyadb.fixture.owner={owner}",
            "--publish", "127.0.0.1::5432", "--shm-size", "1g", args.image,
        ])
        started = True
        print(f"Started owned Apache Cloudberry fixture {name}", flush=True)
        deadline = time.monotonic() + 600
        execute = [
            "docker", "exec", "-i", name, "bash", "-lc",
            "source /usr/local/cloudberry-db/cloudberry-env.sh; psql -X -At -d gpadmin -v ON_ERROR_STOP=1",
        ]
        while True:
            probe = command(execute, sql="SELECT 1;", check=False)
            logs = command(["docker", "logs", name], check=False)
            if probe.returncode == 0 and probe.stdout.strip() == "1" and "DEPLOYMENT SUCCESSFUL" in logs.stdout:
                break
            state = json.loads(command(["docker", "inspect", name]).stdout)[0]["State"]
            if not state["Running"] or time.monotonic() >= deadline:
                raise RuntimeError("Cloudberry initialization failed; inspect the saved fixture log")
            time.sleep(1)
        version = command(execute, sql="SELECT version();").stdout.strip()
        if "Apache Cloudberry" not in version:
            raise RuntimeError("fixture is not Apache Cloudberry")
        print(version, flush=True)
        endpoint = command(["docker", "port", name, "5432/tcp"]).stdout.strip()
        port = endpoint.rsplit(":", 1)[1]
        env = dict(os.environ, CGO_ENABLED="1" if args.race else "0", NYADB_CLOUDBERRY_IT="1", NYADB_CLOUDBERRY_PORT=port,
                   NYADB_CLOUDBERRY_VISUAL_DIR=str(output / "cloudberry-ui"))
        # Both suites use their own nonce schemas. Run sequentially so failures
        # have one original fixture and unambiguous verification receipts.
        for package, log in [
            ("./internal/services/engines", "cloudberry-verification.log"),
            ("./internal/ui/shell", "cloudberry-shell-verification.log"),
        ]:
            with (output / log).open("w") as stream:
                result = subprocess.run(
                    ["go", "test", package, *(["-race"] if args.race else []), "-run", "^TestCloudberry", "-count=1", "-v", "-timeout", "5m"],
                    env=env, cwd=ROOT, text=True, stdout=stream, stderr=subprocess.STDOUT,
                )
            if result.returncode:
                raise RuntimeError(f"{package} failed; inspect {output / log}")
            print(f"Passed {package}", flush=True)
        metadata = {"product": "cloudberry", "image": args.image, "version": version,
                    "runtime_and_native_shell": "passed", "race": args.race, "fixture_owner": owner}
        (output / "cloudberry-fixture-verification.json").write_text(json.dumps(metadata, indent=2) + "\n")
    finally:
        if started:
            logs = command(["docker", "logs", name], check=False)
            (output / "cloudberry-fixture.log").write_text(logs.stdout + logs.stderr)
            inspect = command(["docker", "inspect", name], check=False)
            if inspect.returncode == 0:
                labels = json.loads(inspect.stdout)[0]["Config"]["Labels"] or {}
                if labels.get("nyadb.fixture.owner") == owner and labels.get("nyadb.fixture") == "cloudberry":
                    command(["docker", "rm", "-fv", name])
                    print(f"Removed owned fixture {name}", flush=True)
                else:
                    raise RuntimeError("fixture ownership changed; automatic cleanup refused")


if __name__ == "__main__":
    main()
