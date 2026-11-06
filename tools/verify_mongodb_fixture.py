#!/usr/bin/env python3
"""Run MongoDB tests with isolated standalone and replica-set fixtures."""
import json
import argparse
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "build" / "mongodb"

def run(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, **kwargs)

def verify(topology, image, race):
    name = "nyadb-mongodb-" + uuid.uuid4().hex[:12]
    password = secrets.token_urlsafe(24)
    with tempfile.TemporaryDirectory(prefix="nyadb-mongodb-") as directory:
        envfile = Path(directory) / "fixture.env"
        envfile.write_text("MONGO_INITDB_ROOT_USERNAME=nyadb_it\nMONGO_INITDB_ROOT_PASSWORD=" + password + "\n")
        envfile.chmod(0o600)
        try:
            command = ["docker", "run", "-d", "--name", name, "-p", "127.0.0.1::27017"]
            if topology == "standalone":
                command += ["--env-file", str(envfile)]
            command += [image, "--bind_ip_all"]
            if topology == "replica":
                command += ["--replSet", "nyadbFixture"]
            run(command, stdout=subprocess.DEVNULL)
            mapped = run(["docker", "port", name, "27017/tcp"], capture_output=True).stdout.strip()
            port = mapped.rsplit(":", 1)[1]
            script = 'db.runCommand({ping:1})'
            if topology == "standalone":
                script = 'db.getSiblingDB("admin").auth(process.env.MONGO_INITDB_ROOT_USERNAME,process.env.MONGO_INITDB_ROOT_PASSWORD); db.runCommand({ping:1})'
            deadline = time.monotonic() + 90
            while True:
                state = run(["docker", "inspect", name, "--format", "{{.State.Running}}"], capture_output=True).stdout.strip()
                if state != "true":
                    raise RuntimeError("MongoDB fixture exited; see server log")
                ready = subprocess.run(["docker", "exec", name, "mongosh", "--quiet", "--eval", script], capture_output=True, text=True)
                if ready.returncode == 0:
                    break
                if time.monotonic() > deadline:
                    (OUTPUT / (topology + "-readiness.log")).write_text((ready.stdout + ready.stderr).replace(password, "[hidden]"))
                    raise RuntimeError("MongoDB fixture did not become ready; see readiness log")
                time.sleep(1)
            if topology == "replica":
                run(["docker", "exec", name, "mongosh", "--quiet", "--eval", 'rs.initiate({_id:"nyadbFixture",members:[{_id:0,host:"localhost:27017"}]})'], stdout=subprocess.DEVNULL)
                while True:
                    ready = subprocess.run(["docker", "exec", name, "mongosh", "--quiet", "--eval", 'if (!db.hello().isWritablePrimary) quit(1)'], capture_output=True)
                    if ready.returncode == 0:
                        break
                    if time.monotonic() > deadline:
                        raise RuntimeError("MongoDB replica did not elect a primary")
                    time.sleep(1)
            environment = dict(os.environ, GOWORK="off", CGO_ENABLED="1" if race else "0", NYADB_MONGODB_IT="1", NYADB_MONGODB_PORT=port,
                               NYADB_MONGODB_USERNAME="nyadb_it" if topology == "standalone" else "",
                               NYADB_MONGODB_PASSWORD=password if topology == "standalone" else "",
                               NYADB_MONGODB_CAPTURE_DIR=str(OUTPUT / topology))
            log = OUTPUT / (topology + ".log")
            with log.open("w") as stream:
                command = ["go", "test", "-count=1", "-v", "-timeout", "180s", "-run", "MongoDB", "./internal/services/mongodb", "./internal/services/connections", "./internal/storage/localdb", "./internal/services/dbruntime", "./internal/ui/connectioneditor", "./internal/ui/dataworkspace", "./internal/ui/shell"]
                if race:
                    command.append("-race")
                run(command, env=environment, stdout=stream, stderr=subprocess.STDOUT)
            return {"image": image, "topology": topology, "race": race, "result": "passed", "log": str(log.relative_to(ROOT))}
        finally:
            serverlog = subprocess.run(["docker", "logs", name], capture_output=True, text=True)
            (OUTPUT / (topology + "-server.log")).write_text((serverlog.stdout + serverlog.stderr).replace(password, "[hidden]"))
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

def main():
    global OUTPUT
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="mongo:7.0.28")
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--output", default="build/mongodb")
    args = parser.parse_args()
    OUTPUT = (ROOT / args.output).resolve()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    results = []
    for topology in ("standalone", "replica"):
        results.append(verify(topology, args.image, args.race))
        print(topology + " passed", flush=True)
    (OUTPUT / "report.json").write_text(json.dumps(results, indent=2) + "\n")

if __name__ == "__main__":
    main()
