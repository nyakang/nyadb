#!/usr/bin/env python3
"""Verify against unique loopback-only Redis containers, then remove owned fixtures."""
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "build" / "redis"
OUT.mkdir(parents=True, exist_ok=True)

def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)

def verify(tls):
    name = "nyadb-redis-it-" + uuid.uuid4().hex[:12]
    password = secrets.token_hex(24)
    with tempfile.TemporaryDirectory(prefix="nyadb-redis-", dir=ROOT / "temp") as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        config = f"bind 0.0.0.0\nprotected-mode yes\nsave \"\"\nappendonly no\nuser default off\nuser nyadb on >{password} ~* &* +@all\n"
        if tls:
            run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(folder / "server.key"), "-out", str(folder / "server.crt"), "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1", "-addext", "basicConstraints=critical,CA:TRUE"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (folder / "server.key").chmod(0o600)
            config += "port 0\ntls-port 6379\ntls-cert-file /fixture/server.crt\ntls-key-file /fixture/server.key\ntls-ca-cert-file /fixture/server.crt\ntls-auth-clients no\n"
        (folder / "redis.conf").write_text(config)
        (folder / "redis.conf").chmod(0o600)
        try:
            run(["docker", "run", "-d", "--name", name, "--user", "0:0", "--entrypoint", "redis-server", "-p", "127.0.0.1::6379", "-v", f"{folder}:/fixture:ro", "redis:7.4-alpine", "/fixture/redis.conf"], stdout=subprocess.DEVNULL)
            port = run(["docker", "port", name, "6379/tcp"], capture_output=True).stdout.strip().rsplit(":", 1)[1]
            time.sleep(0.5)
            env = os.environ.copy()
            env.update(GOWORK="off", CGO_ENABLED="0", NYADB_REDIS_IT="1", NYADB_REDIS_PORT=port, NYADB_REDIS_PASSWORD=password, NYADB_REDIS_USERNAME="nyadb", NYADB_REDIS_TLS="1" if tls else "0", NYADB_REDIS_CA=str(folder / "server.crt"), NYADB_REDIS_CAPTURE_DIR=str(OUT))
            race = os.environ.get("NYADB_REDIS_RACE") == "1"
            if race: env["CGO_ENABLED"] = "1"
            label = ("tls" if tls else "tcp") + ("-race" if race else "")
            with (OUT / f"fixture-{label}.log").open("w") as log:
                run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/redis", "./internal/services/dbruntime", "./internal/ui/shell", "-run", "^TestRedis", "-count=1", "-v"], cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
            print(f"Redis {label} fixture passed", flush=True)
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)

if __name__ == "__main__":
    (ROOT / "temp").mkdir(exist_ok=True)
    verify(False)
    verify(True)
