#!/usr/bin/env python3
"""Owned loopback-only Redis Cluster/Sentinel fixtures (plain TCP and verified TLS)."""
import os
from pathlib import Path
import secrets
import signal
import socket
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]

def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, **kwargs)

def verify(tls=False):
    name = "nyadb-redis-topology-" + uuid.uuid4().hex[:12]
    password, sentinel_password = secrets.token_hex(24), secrets.token_hex(24)
    sockets = []
    for _ in range(11):
        sock = socket.socket()
        sock.bind(("127.0.0.1", 0))
        sockets.append(sock)
    ports = [s.getsockname()[1] for s in sockets]
    cluster, data, sentinels = ports[:6], ports[6:8], ports[8:]
    (ROOT / "temp").mkdir(exist_ok=True)
    out = ROOT / "build" / "redis"
    out.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="redis-topology-", dir=ROOT / "temp") as tmp:
        folder = Path(tmp)
        folder.chmod(0o700)
        transport = ""
        if tls:
            run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", str(folder / "server.key"), "-out", str(folder / "server.crt"), "-days", "1", "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1", "-addext", "basicConstraints=critical,CA:TRUE"], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            (folder / "server.key").chmod(0o600)
            transport = "tls-cert-file /fixture/server.crt\ntls-key-file /fixture/server.key\ntls-ca-cert-file /fixture/server.crt\ntls-auth-clients no\ntls-replication yes\n"
        commands = []
        for i, port in enumerate(ports):
            config = "bind 0.0.0.0\nprotected-mode yes\nsave \"\"\nappendonly no\ndir /fixture\n"
            config += (f"port 0\ntls-port {port}\n" + transport) if tls else f"port {port}\n"
            if port in sentinels:
                config += f"user default off\nuser monitor on >{sentinel_password} ~* &* +@all\n"
                config += f"sentinel monitor mymaster 127.0.0.1 {data[0]} 2\nsentinel auth-user mymaster nyadb\nsentinel auth-pass mymaster {password}\nsentinel down-after-milliseconds mymaster 1000\nsentinel failover-timeout mymaster 10000\nsentinel announce-ip 127.0.0.1\nsentinel announce-port {port}\n"
                mode = " --sentinel"
            else:
                config += f"user default off\nuser nyadb on >{password} ~* &* +@all\nmasteruser nyadb\nmasterauth {password}\nreplica-announce-ip 127.0.0.1\nreplica-announce-port {port}\n"
                if port in cluster:
                    # Bus ports need only be reachable inside this single owned container.
                    config += f"cluster-enabled yes\ncluster-config-file node-{port}.conf\ncluster-node-timeout 1000\ncluster-announce-ip 127.0.0.1\ncluster-port {10000+i}\ncluster-announce-bus-port {10000+i}\ncluster-announce-port {0 if tls else port}\n"
                    if tls:
                        config += f"tls-cluster yes\ncluster-announce-tls-port {port}\n"
                elif port == data[1]:
                    config += f"replicaof 127.0.0.1 {data[0]}\n"
                mode = ""
            path = folder / f"{port}.conf"
            path.write_text(config)
            path.chmod(0o600)
            commands.append(f"redis-server /fixture/{port}.conf{mode} > /fixture/{port}.log 2>&1 &")
        (folder / "start.sh").write_text("#!/bin/sh\n" + "\n".join(commands) + "\nwait\n")
        for sock in sockets:
            sock.close()
        try:
            args = ["docker", "run", "-d", "--name", name, "--user", "0:0", "--entrypoint", "sh"]
            for port in ports:
                args += ["-p", f"127.0.0.1:{port}:{port}"]
            run(args + ["-v", f"{folder}:/fixture", "redis:7.4-alpine", "/fixture/start.sh"], stdout=subprocess.DEVNULL)
            cli_env = os.environ.copy()
            cli_env["REDISCLI_AUTH"] = password
            cli = ["docker", "exec", "-e", "REDISCLI_AUTH", name, "redis-cli", "--user", "nyadb"]
            if tls:
                cli += ["--tls", "--cacert", "/fixture/server.crt"]
            for _ in range(60):
                reply = subprocess.run(cli + ["-p", str(cluster[0]), "PING"], env=cli_env, text=True, capture_output=True)
                if reply.returncode == 0 and reply.stdout.strip() == "PONG":
                    break
                time.sleep(0.2)
            run(cli + ["--cluster", "create"] + [f"127.0.0.1:{p}" for p in cluster] + ["--cluster-replicas", "1", "--cluster-yes"], env=cli_env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            env = os.environ.copy()
            env.update(CGO_ENABLED="0", GOWORK="off", NYADB_REDIS_TOPOLOGY_IT="1", NYADB_REDIS_CLUSTER_PORTS=",".join(map(str, cluster)), NYADB_REDIS_SENTINEL_PORTS=",".join(map(str, sentinels)), NYADB_REDIS_DATA_PORTS=",".join(map(str, data)), NYADB_REDIS_PASSWORD=password, NYADB_REDIS_SENTINEL_PASSWORD=sentinel_password, NYADB_REDIS_TOPOLOGY_TLS="1" if tls else "0", NYADB_REDIS_CA=str(folder / "server.crt"))
            race = os.environ.get("NYADB_REDIS_RACE") == "1"
            if race:
                env["CGO_ENABLED"] = "1"
            label = ("tls" if tls else "tcp") + ("-race" if race else "")
            with (out / f"topology-{label}.log").open("w") as log:
                run(["go", "test"] + (["-race"] if race else []) + ["./internal/services/redis", "-run", "^TestRedisTopology", "-count=1", "-v", "-timeout", "180s"], env=env, cwd=ROOT, stdout=log, stderr=subprocess.STDOUT)
            print(f"Redis Cluster/Sentinel {label} passed", flush=True)
        finally:
            subprocess.run(["docker", "rm", "-f", name], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)

if __name__ == "__main__":
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    verify(False)
    verify(True)
