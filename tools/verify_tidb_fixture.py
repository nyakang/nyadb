#!/usr/bin/env python3
"""Owned TiDB/PD/TiKV fixture with verified SQL TLS and a disposable test user."""
import argparse
import json
import os
from pathlib import Path
import secrets
import signal
import subprocess
import tempfile
import time
from urllib.request import Request, urlopen
import uuid

ROOT = Path(__file__).resolve().parents[1]

def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)

def remove_owned(resource, name, nonce):
    """Names alone never authorize fixture cleanup."""
    result = subprocess.run(["docker", resource, "inspect", name], capture_output=True, text=True, check=False)
    if result.returncode != 0:
        return
    try:
        item = json.loads(result.stdout)[0]
        labels = (item.get("Config", {}).get("Labels", {}) if resource == "container" else item.get("Labels", {})) or {}
        if labels.get("nyadb.fixture.nonce") != nonce or labels.get("nyadb.fixture") != "tidb":
            return
        identifier = item["Id"]
    except (ValueError, KeyError, IndexError, TypeError):
        return
    command = ["docker", "rm", "-f", identifier] if resource == "container" else ["docker", "network", "rm", identifier]
    subprocess.run(command, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)

def verify(hold=False):
    nonce = uuid.uuid4().hex[:12]
    network = "nyadb-tidb-it-" + nonce
    names = [network + suffix for suffix in ("-pd", "-tikv", "-sql")]
    owned = []
    (ROOT / "temp" / "tidb").mkdir(parents=True, exist_ok=True)
    out = ROOT / "build" / "tidb"
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="fixture-", dir=ROOT / "temp" / "tidb") as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        password = secrets.token_hex(24)
        run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(folder / "server.key"), "-out", str(folder / "ca.pem"), "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1,DNS:localhost", "-addext", "basicConstraints=critical,CA:TRUE"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        (folder / "server.key").chmod(0o600)
        (folder / "pd.toml").write_text('[replication]\nmax-replicas = 1\n')
        (folder / "tidb.toml").write_text('[security]\nssl-ca = "/fixture/ca.pem"\nssl-cert = "/fixture/ca.pem"\nssl-key = "/fixture/server.key"\n[performance]\nmax-memory = 536870912\n')
        (folder / "tikv.toml").write_text('[storage.block-cache]\ncapacity = "64MB"\n[rocksdb]\nmax-background-jobs = 2\n[raftdb]\nmax-background-jobs = 2\n[server]\ngrpc-concurrency = 2\n[readpool.unified]\nmin-thread-count = 1\nmax-thread-count = 2\n')
        run(["docker", "network", "create", "--label", "nyadb.fixture=tidb", "--label", "nyadb.fixture.nonce="+nonce, network], stdout=subprocess.DEVNULL)
        try:
            def start(index, image, args, publish=()):
                owned.append(names[index])
                cmd = ["docker", "run", "-d", "--name", names[index], "--network", network, "--label", "nyadb.fixture=tidb", "--label", "nyadb.fixture.nonce="+nonce, "--user", "0:0"]
                for port in publish:
                    cmd += ["-p", f"127.0.0.1::{port}"]
                cmd += ["-v", f"{folder}:/fixture:ro"]
                run(cmd + [image] + args, stdout=subprocess.DEVNULL)
            start(0, "pingcap/pd:v8.5.3", ["--name="+names[0], "--client-urls=http://0.0.0.0:2379", "--advertise-client-urls=http://"+names[0]+":2379", "--peer-urls=http://0.0.0.0:2380", "--advertise-peer-urls=http://"+names[0]+":2380", "--initial-cluster="+names[0]+"=http://"+names[0]+":2380", "--data-dir=/data", "--config=/fixture/pd.toml"], [2379])
            pd_port = run(["docker", "port", names[0], "2379/tcp"], capture_output=True).stdout.strip().rsplit(":", 1)[1]
            for attempt in range(60):
                try:
                    request = Request(f"http://127.0.0.1:{pd_port}/pd/api/v1/health")
                    with urlopen(request, timeout=2):
                        break
                except Exception:
                    if attempt == 59:
                        raise
                    time.sleep(0.5)
            start(1, "pingcap/tikv:v8.5.3", ["--addr=0.0.0.0:20160", "--advertise-addr="+names[1]+":20160", "--status-addr=0.0.0.0:20180", "--pd="+names[0]+":2379", "--data-dir=/data", "--config=/fixture/tikv.toml"])
            start(2, "pingcap/tidb:v8.5.3", ["--store=tikv", "--path="+names[0]+":2379", "--host=0.0.0.0", "-P=4000", "--config=/fixture/tidb.toml"], [4000])
            port = run(["docker", "port", names[2], "4000/tcp"], capture_output=True).stdout.strip().rsplit(":", 1)[1]
            env = os.environ.copy()
            env.update(CGO_ENABLED="0", GOWORK="off", NYADB_TIDB_IT="1", NYADB_TIDB_PORT=port, NYADB_TIDB_PASSWORD=password, NYADB_TIDB_CA_FILE=str(folder / "ca.pem"), NYADB_TIDB_VISUAL_DIR=str(out))
            run(["go", "run", "./tools/tidbfixture"], cwd=ROOT, env=env)
            if hold:
                path = folder / "environment.json"
                path.write_text(json.dumps({key:value for key,value in env.items() if key.startswith("NYADB_TIDB_")}))
                path.chmod(0o600)
                print("Fixture ready; private environment file:", path, flush=True)
                while True:
                    time.sleep(1)
            race = os.environ.get("NYADB_TIDB_RACE") == "1"
            if race:
                env["CGO_ENABLED"] = "1"
            label = "race" if race else "tcp-tls"
            with (out / f"{label}-verification.log").open("w") as log:
                run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/mysql", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^TestTiDB", "-count=1", "-timeout", "180s", "-v"], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
            print("TiDB real-cluster verification passed:", label, flush=True)
        finally:
            for name in reversed(owned):
                remove_owned("container", name, nonce)
            remove_owned("network", network, nonce)

if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, terminate)
    args = argparse.ArgumentParser()
    args.add_argument("--hold", action="store_true", help="retain an owned fixture for interactive verification until interrupted")
    verify(args.parse_args().hold)
