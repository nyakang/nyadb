#!/usr/bin/env python3
"""Own a disposable Linux Secret Service, display and MySQL 8.4 fixture.

No system installation. Secrets are stdin-only at creation and kept in a 0600
ignored file for opt-in subsequent commands; manifests/logs exclude secrets.
"""
import argparse
import fcntl
import hashlib
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import subprocess
import time

REPO = Path(__file__).resolve().parents[1]
BASE = REPO / "build/entry-acceptance"
LABEL = "org.nyadb.entry-acceptance"


def command(args, *, env=None, data=None, timeout=120):
    result = subprocess.run(args, cwd=REPO, env=env, input=data, capture_output=True,
                            text=True, timeout=timeout)
    if result.returncode:
        # SQL input and environment may contain secrets; never echo them.
        raise RuntimeError(f"{Path(args[0]).name} failed (exit {result.returncode})")
    return result.stdout.strip()


def birth(pid):
    try:
        # comm can contain spaces; starttime is field 22 after its final ')'.
        return Path(f"/proc/{pid}/stat").read_text().rsplit(")", 1)[1].split()[19]
    except (OSError, IndexError):
        return None


def manifest(root, value):
    temporary = root / "run.json.tmp"
    temporary.write_text(json.dumps(value, indent=2) + "\n")
    temporary.replace(root / "run.json")


def validate(root):
    root = root.resolve()
    if root.parent != BASE.resolve() or not (root / ".owned").is_file():
        raise RuntimeError("Not an owned acceptance directory")
    state = json.loads((root / "run.json").read_text())
    if (root / ".owned").read_text().strip() != state["id"]:
        raise RuntimeError("Run identity mismatch")
    return root, state


def environment(root, state):
    env = dict(os.environ, GOWORK="off", NYADB_ENTRY_IT="1", NYADB_ENTRY_ROOT=str(root),
               NYADB_ENTRY_PORT=str(state["port"]), DISPLAY=state["display"],
               DBUS_SESSION_BUS_ADDRESS=f"unix:path={root}/runtime/bus",
               XDG_CONFIG_HOME=str(root / "config"), XDG_CACHE_HOME=str(root / "cache"),
               XDG_DATA_HOME=str(root / "data"), XDG_RUNTIME_DIR=str(root / "runtime"),
               LANG="zh_CN.UTF-8", LANGUAGE="zh_CN", NO_AT_BRIDGE="1")
    env.update(json.loads((root / "secrets.json").read_text()))
    deps = BASE / "deps/root/usr/lib/x86_64-linux-gnu"
    env["LD_LIBRARY_PATH"] = str(deps) + ":" + os.environ.get("LD_LIBRARY_PATH", "")
    return env


def spawn(root, state, name, args, env, data=None):
    with (root / "evidence" / f"{name}.log").open("ab") as log:
        proc = subprocess.Popen(args, cwd=REPO, env=env, stdin=subprocess.PIPE if data else subprocess.DEVNULL,
                                stdout=log, stderr=log, start_new_session=True)
    state["processes"][name] = {"pid": proc.pid, "birth": birth(proc.pid)}
    manifest(root, state)
    if data:
        proc.stdin.write(data.encode())
        proc.stdin.close()
    return proc


def terminate(identity):
    pid = identity["pid"]
    if not identity["birth"] or birth(pid) != identity["birth"]:
        return
    try:
        os.killpg(pid, signal.SIGTERM)
    except ProcessLookupError:
        return
    for _ in range(40):
        if birth(pid) != identity["birth"]:
            return
        time.sleep(.05)
    if birth(pid) == identity["birth"]:
        try:
            os.killpg(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass


def stop(root, state):
    for ident in reversed(list(state["processes"].values())):
        terminate(ident)
    if state.get("container"):
        label = subprocess.run(["docker", "inspect", "--format", '{{index .Config.Labels "' + LABEL + '"}}',
                                state["container"]], capture_output=True, text=True)
        if label.returncode == 0 and label.stdout.strip() == state["id"]:
            command(["docker", "rm", "-f", state["container"]])
    (root / "secrets.json").unlink(missing_ok=True)
    state["stopped"] = True
    state["cleanup"] = {"owned_processes_stopped": True, "fixture_secrets_removed": True}
    manifest(root, state)


def dependencies():
    daemon = BASE / "deps/root/usr/bin/gnome-keyring-daemon"
    if not daemon.exists():
        packages = BASE / "deps/packages"
        packages.mkdir(parents=True, exist_ok=True)
        result = subprocess.run(["apt-get", "download", "gnome-keyring", "libgck-1-0", "libgcr-base-3-1"],
                                cwd=packages, capture_output=True)
        if result.returncode:
            raise RuntimeError("Local dependency download failed; no packages were installed")
        for deb in packages.glob("*.deb"):
            command(["dpkg-deb", "-x", str(deb), str(BASE / "deps/root")])
    return daemon


def start():
    daemon = dependencies()
    run_id = secrets.token_hex(6)
    root = BASE / ("r-" + run_id)
    root.mkdir(parents=True, mode=0o700)
    for sub in ("config", "cache", "data", "runtime", "evidence"):
        (root / sub).mkdir(mode=0o700)
    (root / ".owned").write_text(run_id + "\n")
    secret_path = root / "secrets.json"
    secret_path.touch(mode=0o600)
    secret_path.write_text(json.dumps({"NYADB_ENTRY_PASSWORD": secrets.token_hex(24),
                                      "NYADB_ENTRY_READ_PASSWORD": secrets.token_hex(24),
                                      "NYADB_ENTRY_KEYRING_PASSWORD": secrets.token_hex(24)}))
    state = {"id": run_id, "container": "nyadb-entry-" + run_id, "processes": {},
             "port": 0, "display": "", "commands": [], "baseline": command(["git", "rev-parse", "HEAD"])}
    manifest(root, state)
    env = environment(root, state)
    try:
        bus_config = root / "runtime/bus.conf"
        bus_config.write_text('<busconfig><type>session</type><listen>unix:path=' + str(root) +
                              '/runtime/bus</listen><auth>EXTERNAL</auth><policy context="default">'
                              '<allow send_destination="*"/><allow receive_sender="*"/>'
                              '<allow own="*"/></policy></busconfig>')
        spawn(root, state, "dbus", ["dbus-daemon", "--nofork", "--config-file=" + str(bus_config)], env)
        for _ in range(100):
            if (root / "runtime/bus").exists():
                break
            time.sleep(.05)
        spawn(root, state, "xvfb", ["Xvfb", "-displayfd", "1", "-screen", "0", "1600x1000x24", "-ac", "-nolisten", "tcp"], env)
        display_path = root / "evidence/xvfb.log"
        for _ in range(100):
            lines = display_path.read_text().splitlines()
            number = next((line for line in reversed(lines) if line.isdigit()), None)
            if number:
                state["display"] = ":" + number
                break
            time.sleep(.05)
        if not state["display"]:
            raise RuntimeError("Private display did not start")
        manifest(root, state)
        env = environment(root, state)
        spawn(root, state, "openbox", ["openbox", "--sm-disable"], env)
        spawn(root, state, "keyring", [str(daemon), "--foreground", "--components=secrets", "--unlock", "--control-directory=" + str(root / "runtime/keyring")], env,
              env["NYADB_ENTRY_KEYRING_PASSWORD"] + "\n")
        command(["docker", "run", "--detach", "--rm", "--name", state["container"],
                 "--label", LABEL + "=" + run_id, "--publish", "127.0.0.1::3306",
                 "--env", "MYSQL_ALLOW_EMPTY_PASSWORD=yes", "mysql:8.4", "--log-bin-trust-function-creators=1"])
        state["image"] = command(["docker", "inspect", "--format", "{{.Image}}", state["container"]])
        for _ in range(90):
            ping = subprocess.run(["docker", "exec", state["container"], "mysqladmin", "ping", "-uroot", "--silent"], capture_output=True)
            if ping.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Disposable MySQL did not become ready")
        state["port"] = int(command(["docker", "port", state["container"], "3306/tcp"]).rsplit(":", 1)[1])
        # Passwords are hex, so these stdin-only SQL literals require no escaping.
        sql = "CREATE DATABASE entry_source; CREATE DATABASE entry_target; CREATE DATABASE entry_dump;\n"
        sql += "CREATE USER 'entry_admin'@'%' IDENTIFIED BY '" + env["NYADB_ENTRY_PASSWORD"] + "';\n"
        sql += "GRANT ALL PRIVILEGES ON *.* TO 'entry_admin'@'%' WITH GRANT OPTION;\n"
        sql += "CREATE USER 'entry_read'@'%' IDENTIFIED BY '" + env["NYADB_ENTRY_READ_PASSWORD"] + "';\n"
        sql += "GRANT SELECT, SHOW VIEW, EVENT, TRIGGER ON entry_source.* TO 'entry_read'@'%';\n"
        sql += "USE entry_source; CREATE TABLE samples(id int PRIMARY KEY, amount decimal(40,20), optional text NULL, payload varbinary(64));\n"
        sql += "INSERT INTO samples VALUES(1,12345678901234567890.12345678901234567890,NULL,X'0001FF'),(2,-0.00000000000000000001,'fixture',X'');\n"
        sql += "CREATE FUNCTION entry_function() RETURNS INT DETERMINISTIC RETURN 42; CREATE PROCEDURE entry_procedure() SELECT 42; CREATE EVENT entry_event ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 42;\n"
        command(["docker", "exec", "-i", state["container"], "mysql", "-uroot"], data=sql)
        state["dependencies"] = [{"file": deb.name, "sha256": hashlib.sha256(deb.read_bytes()).hexdigest()} for deb in sorted((BASE / "deps/packages").glob("*.deb"))]
        state["versions"] = {"go": command(["go", "version"]), "keyring": command([str(daemon), "--version"], env=env),
                             "mysql": command(["docker", "exec", state["container"], "mysql", "--version"])}
        manifest(root, state)
        return root, state
    except BaseException:
        stop(root, state)
        raise


def execute(root, state, args, name, overrides=None):
    env = environment(root, state)
    if overrides:
        env.update(overrides)
    if name == "custom":
        name += "-" + secrets.token_hex(4)
    with (root / "evidence" / (name + ".log")).open("wb") as log:
        proc = subprocess.Popen(args, cwd=REPO, env=env, stdout=log, stderr=log, start_new_session=True)
    identity = {"pid": proc.pid, "birth": birth(proc.pid)}
    state["processes"]["command"] = identity
    manifest(root, state)
    interrupted = False
    try:
        code = proc.wait()
    except BaseException:
        interrupted = True
        terminate(identity)
        code = proc.wait(timeout=10)
        raise
    finally:
        state.update(json.loads((root / "run.json").read_text()))
        state["processes"].pop("command", None)
        state["commands"].append({"name": name, "exit": code, "interrupted": interrupted})
        manifest(root, state)
    print(f"{name}: exit {code}", flush=True)
    if code:
        raise RuntimeError(f"{name} failed; see its evidence log")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep", action="store_true", help="retain isolated resources for further fixture commands")
    parser.add_argument("--run", type=Path, help="reuse only this tool's owned isolated run")
    parser.add_argument("--stop", action="store_true")
    parser.add_argument("--native", action="store_true")
    parser.add_argument("--start-only", action="store_true")
    parser.add_argument("command", nargs=argparse.REMAINDER)
    opts = parser.parse_args()
    if opts.stop and not opts.run:
        parser.error("--stop requires --run; no new fixture will be created")
    if opts.run and not (opts.command or opts.stop or opts.start_only):
        parser.error("reuse a run with --stop or an explicit command")
    def interrupted(*_):
        raise KeyboardInterrupt()
    signal.signal(signal.SIGTERM, interrupted)
    root, state = validate(opts.run) if opts.run else start()
    lock = (root / ".command.lock").open("w")
    fcntl.flock(lock, fcntl.LOCK_EX)
    root, state = validate(root)
    print(f"Owned run: {root}", flush=True)
    if opts.stop:
        stop(root, state)
        return
    try:
        if opts.command:
            args = opts.command[1:] if opts.command[0] == "--" else opts.command
            execute(root, state, args, "custom")
        elif not opts.start_only:
            execute(root, state, ["go", "run", "./tools/entryfixture", "write"], "vault-write")
            execute(root, state, ["go", "run", "./tools/entryfixture", "read"], "vault-cross-process")
            execute(root, state, ["go", "run", "./tools/entryfixture", "keys"], "vault-key-failures")
            terminate(state["processes"].pop("keyring"))
            execute(root, state, ["go", "run", "./tools/entryfixture", "unavailable"], "vault-unavailable")
            execute(root, state, ["go", "test", "./internal/ui/shell", "-run", "^TestEntryAcceptanceVaultUnavailable", "-count=1", "-v", "-timeout=20m"], "vault-unavailable-ui", {"NYADB_ENTRY_KEYRING_DOWN": "1"})
            env = environment(root, state)
            spawn(root, state, "keyring", [str(dependencies()), "--foreground", "--components=secrets", "--unlock", "--control-directory=" + str(root / "runtime/keyring")], env, env["NYADB_ENTRY_KEYRING_PASSWORD"] + "\n")
            execute(root, state, ["go", "run", "./tools/entryfixture", "read"], "vault-restart")
            execute(root, state, ["go", "test", "./internal/ui/shell", "-run", "^TestEntryAcceptance", "-count=1", "-timeout=20m", "-v"], "mysql-shell")
            if opts.native:
                execute(root, state, ["go", "tool", "mygo", "build"], "formal-build")
                execute(root, state, ["/usr/bin/python3", "tools/capture_entry_acceptance.py"], "native")
    finally:
        if not opts.keep:
            state.update(json.loads((root / "run.json").read_text()))
            stop(root, state)
        lock.close()


if __name__ == "__main__":
    main()
