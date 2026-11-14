#!/usr/bin/env python3
"""Verify OpenTenBase in one owned container with GTM, CN and two real DNs.

The image is compiled from pinned licensed upstream sources kept in ignored
temp/opentenbase-fixture. Only the CN is published on a random loopback port.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import secrets
import subprocess


ROOT = Path(__file__).resolve().parents[1]


def command(args: list[str], *, input_text: str | None = None, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, input=input_text, text=True, capture_output=True, check=check, cwd=ROOT)


INITIALIZE = r"""set -eu
initgtm -Z gtm -D /data/opentenbase/gtm
cat >> /data/opentenbase/gtm/gtm.conf <<'CONF'
listen_addresses = '127.0.0.1'
port = 6666
nodename = 'gtm'
startup = ACT
CONF
gtm_ctl -Z gtm -D /data/opentenbase/gtm -l /data/opentenbase/gtm.log start
for item in 'cn001 coordinator 11000 11001 11002' 'dn001 datanode 12000 12001 12002' 'dn002 datanode 13000 13001 13002'; do
    set -- $item
    node=$1
    type=$2
    port=$3
    pool=$4
    forward=$5
    initdb -D /data/opentenbase/$node --nodename=$node --nodetype=$type --master_gtm_nodename=gtm --master_gtm_ip=127.0.0.1 --master_gtm_port=6666 --no-locale -E UTF8 -A trust -U opentenbase
    cat >> /data/opentenbase/$node/postgresql.conf <<CONF
listen_addresses = '*'
port = $port
pooler_port = $pool
forward_port = $forward
shared_buffers = '64MB'
max_connections = 40
max_wal_senders = 4
max_pool_size = 200
logging_collector = off
log_destination = 'stderr'
CONF
    echo 'host all all 0.0.0.0/0 trust' >> /data/opentenbase/$node/pg_hba.conf
    pg_ctl -D /data/opentenbase/$node -Z $type -l /data/opentenbase/$node.log -W start
    attempts=0
    until psql -X -At -h 127.0.0.1 -p "$port" -d postgres -c 'SELECT 1' >/dev/null 2>&1; do
        attempts=$((attempts + 1))
        if [ "$attempts" -ge 60 ]; then
            cat /data/opentenbase/$node.log
            exit 1
        fi
        sleep 1
    done
done
for item in 'cn001 11000 11002' 'dn001 12000 12002' 'dn002 13000 13002'; do
    set -- $item
    node=$1
    port=$2
    forward=$3
    psql -X -h 127.0.0.1 -p $port -d postgres -v ON_ERROR_STOP=1 <<SQL
ALTER NODE $node WITH (HOST='127.0.0.1', PORT=$port, FORWARD=$forward);
SQL
    for peer in 'cn001 coordinator 11000 11002' 'dn001 datanode 12000 12002' 'dn002 datanode 13000 13002'; do
        set -- $peer
        if [ "$1" != "$node" ]; then
            psql -X -h 127.0.0.1 -p $port -d postgres -v ON_ERROR_STOP=1 <<SQL
CREATE NODE $1 WITH (TYPE='$2',HOST='127.0.0.1',PORT=$3,FORWARD=$4);
SQL
        fi
    done
    psql -X -h 127.0.0.1 -p $port -d postgres -v ON_ERROR_STOP=1 -c 'SELECT pgxc_pool_reload();'
done
psql -X -h 127.0.0.1 -p 11000 -d postgres -v ON_ERROR_STOP=1 <<'SQL'
CREATE DEFAULT NODE GROUP default_group WITH (dn001,dn002);
CREATE SHARDING GROUP TO GROUP default_group;
SELECT version();
SQL
"""


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="nyadb-opentenbase-fixture:5.0-community")
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--keep-on-failure", action="store_true", help="retain only this run's owned fixture for debugging")
    args = parser.parse_args()
    image_info = json.loads(command(["docker", "image", "inspect", args.image]).stdout)[0]
    owner = secrets.token_hex(6)
    name = f"nyadb-opentenbase-{owner}"
    output = ROOT / "build/mysql-products"
    output.mkdir(parents=True, exist_ok=True)
    started = False
    verified = False
    try:
        command(["docker", "run", "-d", "--name", name, "--label", "nyadb.fixture=opentenbase",
                 "--label", f"nyadb.fixture.owner={owner}", "--publish", "127.0.0.1::11000",
                 "--shm-size", "1g", args.image])
        started = True
        print(f"Started owned OpenTenBase fixture {name}", flush=True)
        initialized = command(["docker", "exec", "-i", name, "bash", "-s"], input_text=INITIALIZE, check=False)
        (output / "opentenbase-initialization.log").write_text(initialized.stdout + initialized.stderr)
        if initialized.returncode:
            raise RuntimeError("OpenTenBase initialization failed; inspect saved initialization log")
        execute = ["docker", "exec", "-i", name, "psql", "-X", "-At", "-h", "127.0.0.1", "-p", "11000", "-d", "postgres", "-v", "ON_ERROR_STOP=1"]
        version = command(execute, input_text="SELECT version();").stdout.strip()
        if "OpenTenBase" not in version:
            raise RuntimeError("fixture is not OpenTenBase")
        print(version, flush=True)
        port = command(["docker", "port", name, "11000/tcp"]).stdout.strip().rsplit(":", 1)[1]
        env = dict(os.environ, CGO_ENABLED="1" if args.race else "0", NYADB_OPENTENBASE_IT="1",
                   NYADB_OPENTENBASE_PORT=port, NYADB_OPENTENBASE_VISUAL_DIR=str(output / "opentenbase-ui"))
        for package, log in [("./internal/services/engines", "opentenbase-verification.log"),
                             ("./internal/ui/shell", "opentenbase-shell-verification.log")]:
            with (output / log).open("w") as stream:
                result = subprocess.run(["go", "test", package, *(["-race"] if args.race else []),
                                         "-run", "^TestOpenTenBase", "-count=1", "-v", "-timeout", "5m"],
                                        env=env, cwd=ROOT, text=True, stdout=stream, stderr=subprocess.STDOUT)
            required = {
                "./internal/services/engines": {
                    "TestOpenTenBaseNativeMetadataAndDefinitionRoundTripFixture",
                    "TestOpenTenBaseDataTransactionsPlansAndOriginalLeasesFixture",
                    "TestOpenTenBaseNativeEditKeyEligibilityFixture",
                },
                "./internal/ui/shell": {
                    "TestOpenTenBaseSessionSharedGridAndDisconnectFixture",
                    "TestOpenTenBaseNativeChooserEditorAndSavedProfileIdentity",
                },
            }[package]
            saved = (output / log).read_text()
            passed = set(re.findall(r"^--- PASS: (\w+) \(", saved, re.MULTILINE))
            if result.returncode or not required.issubset(passed) or "--- SKIP:" in saved or "DATA RACE" in saved:
                raise RuntimeError(f"{package} failed; inspect {output / log}")
            print(f"Passed {package}", flush=True)
        metadata = {"product": "opentenbase", "image": args.image, "image_id": image_info["Id"], "version": version,
                    "runtime_and_native_shell": "passed", "race": args.race, "fixture_owner": owner}
        source_manifest = ROOT / "temp/opentenbase-fixture/sources.json"
        if source_manifest.exists() and (image_info["Config"].get("Labels") or {}).get("nyadb.fixture.build") == "upstream-community":
            metadata["source_manifest"] = json.loads(source_manifest.read_text())
        (output / "opentenbase-fixture-verification.json").write_text(json.dumps(metadata, indent=2) + "\n")
        verified = True
    finally:
        if started:
            logs = command(["docker", "exec", name, "find", "/data/opentenbase", "-type", "f", "-name", "*.log", "-exec", "cat", "{}", "+"], check=False)
            (output / "opentenbase-fixture.log").write_text(logs.stdout + logs.stderr)
            inspect = command(["docker", "inspect", name], check=False)
            if inspect.returncode == 0:
                labels = json.loads(inspect.stdout)[0]["Config"]["Labels"] or {}
                if labels.get("nyadb.fixture.owner") == owner and labels.get("nyadb.fixture") == "opentenbase":
                    if args.keep_on_failure and not verified:
                        print(f"Retained owned fixture {name} for debugging (owner {owner})", flush=True)
                    else:
                        command(["docker", "rm", "-fv", name])
                        print(f"Removed owned fixture {name}", flush=True)
                else:
                    raise RuntimeError("fixture ownership changed; automatic cleanup refused")


if __name__ == "__main__":
    main()
