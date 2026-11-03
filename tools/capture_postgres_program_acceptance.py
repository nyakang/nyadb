#!/usr/bin/env python3
"""Capture formal PostgreSQL routine/sequence browsing in an owned Linux run.

The original program stays closed. A unique, labelled PostgreSQL fixture is
created and removed here; application config and screenshots belong to the run.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
import uuid

from capture_alignment_gui_linux import NativeCapture
from verify_entry_acceptance import REPO, command, environment, manifest, spawn, terminate, validate


def source_snapshot():
    paths = subprocess.check_output(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], cwd=REPO).decode().split("\0")
    files = {p: hashlib.sha256((REPO / p).read_bytes()).hexdigest()
             for p in sorted(set(paths)) if p and (REPO / p).is_file()}
    return dict(head=subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=REPO).decode().strip(), files=files,
                tree_sha256=hashlib.sha256(json.dumps(files, sort_keys=True).encode()).hexdigest())


def cleanup_resources(root, state, cap, container, evidence):
    """Attempt every owned cleanup even if capture, termination or I/O fails."""
    result, errors = {}, []

    def attempt(name, action):
        try:
            action()
            result[name] = True
        except BaseException as error:
            result[name] = False
            errors.append(error)

    if cap is not None:
        attempt("capture_closed", cap.close)
    if "app" in state["processes"]:
        def stop_app():
            terminate(state["processes"]["app"])
            del state["processes"]["app"]
        attempt("app_stopped", stop_app)
    attempt("manifest_saved", lambda: manifest(root, state))
    if container is not None:
        def remove_container():
            actual = command(["docker", "inspect", "--format", '{{index .Config.Labels "org.nyadb.program-fixture"}}', container])
            if actual != state["id"]:
                raise RuntimeError("Refusing to remove a PostgreSQL fixture with another owner")
            command(["docker", "rm", "-f", container])
            if subprocess.run(["docker", "inspect", container], capture_output=True).returncode == 0:
                raise RuntimeError("Owned PostgreSQL fixture remains")
        attempt("container_removed", remove_container)
    result["errors"] = [str(error) for error in errors]
    attempt("cleanup_record_saved", lambda: (evidence / "cleanup.json").write_text(json.dumps(result, indent=2) + "\n"))
    if errors:
        raise BaseExceptionGroup("PostgreSQL capture cleanup failed", errors)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--explore", action="store_true")
    opts = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    if "app" in state["processes"] or (root / "config/NyaDB/config.sqlite").exists():
        raise RuntimeError("PostgreSQL capture requires a fresh owned config and process")
    env = environment(root, state)
    evidence = root / "evidence/postgres-program-native" / ("capture-" + uuid.uuid4().hex[:12])
    evidence.mkdir(parents=True)
    name = "nyadb-program-" + state["id"]
    label = "org.nyadb.program-fixture"
    container = None
    cap = None
    window = None
    binary = REPO / "build/linux-amd64/nyadb"
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    source = source_snapshot()
    (evidence / "source-check.json").write_text(json.dumps(source, indent=2) + "\n")

    def capture(name):
        cap.capture(name, window, "postgres-programs", dict(binary_sha256=digest,
            real_postgres=True, schema="browse", original_comparison=False, scale="unknown", loading=False,
            source_tree_sha256=source["tree_sha256"], visual_review_required=True))

    def click(x, y):
        cap.actions.append(dict(action="click", client_x=x, client_y=y))
        cap.click(window, x, y)
        time.sleep(.4)

    def key(*keys):
        cap.actions.append(dict(action="key", keys=list(keys)))
        cap.key(*keys)
        time.sleep(.4)

    def psql(sql):
        return command(["docker", "exec", "-i", container, "psql", "-X", "-v", "ON_ERROR_STOP=1", "-At", "-U", "nyadb_it", "-d", "nyadb_it"], data=sql)

    try:
        container = command(["docker", "run", "--rm", "-d", "--name", name, "--label", label + "=" + state["id"],
            "-p", "127.0.0.1::5432", "-e", "POSTGRES_USER=nyadb_it", "-e", "POSTGRES_DB=nyadb_it",
            "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:17-alpine"])
        (evidence / "container.json").write_text(json.dumps(dict(id=container, name=name, label=label, run_id=state["id"]), indent=2) + "\n")
        for _ in range(60):
            ready = subprocess.run(["docker", "exec", container, "pg_isready", "-U", "nyadb_it"], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Owned PostgreSQL fixture did not become ready")
        env["NYADB_PROGRAM_PORT"] = command(["docker", "port", container, "5432/tcp"]).rsplit(":", 1)[1]
        psql("""CREATE SCHEMA browse;
CREATE SEQUENCE browse.counter AS bigint START WITH 9223372036854775805 CACHE 17;
CREATE TABLE browse.effects(value text);
CREATE FUNCTION browse.over(arg integer) RETURNS bigint LANGUAGE SQL AS $$SELECT nextval('browse.counter')$$;
CREATE FUNCTION browse.over(arg text) RETURNS text LANGUAGE SQL AS $$SELECT arg$$;
CREATE PROCEDURE browse.mutate(arg integer) LANGUAGE plpgsql AS $$BEGIN INSERT INTO browse.effects VALUES ('executed'); END$$;
COMMENT ON SEQUENCE browse.counter IS '精确大整数序列';
""")
        before = psql("SELECT last_value,is_called,(SELECT count(*) FROM browse.effects) FROM browse.counter")
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-postgres-programs"], cwd=REPO, env=env, check=True)
        cap = NativeCapture(env["DISPLAY"], evidence, binary)
        app = spawn(root, state, "app", [str(binary)], env)
        manifest(root, state)
        (evidence / "app-identity.json").write_text(json.dumps(dict(state["processes"]["app"], binary_sha256=digest, run_id=state["id"]), indent=2) + "\n")
        window = cap.wait_window("^NyaDB$", pid=app.pid)
        if hashlib.sha256(Path(f"/proc/{app.pid}/exe").read_bytes()).hexdigest() != digest:
            raise RuntimeError("Running formal binary differs from build")
        cap.move_client(window, 30, 50)
        cap.resize(window, 1280, 800)
        time.sleep(1)
        click(120, 128)
        key("Return")
        time.sleep(1)
        capture("connected-1280")
        click(110, 150)
        key("Return")
        capture("catalog-1280")
        click(110, 170)
        key("Return")
        time.sleep(1)
        capture("objects-1280")
        if opts.explore:
            return
        click(120, 234)
        capture("functions-1280")
        click(360, 190)
        key("Return")
        capture("function-definition-1280")
        # Opening a document reveals its navigation node and expands its
        # category. Return to the object browser, then collapse it before
        # addressing the next category row. Tree selection alone preserves
        # the active definition document.
        click(285, 109)
        click(63, 234)
        click(120, 255)
        click(360, 190)
        key("Return")
        capture("procedure-definition-1280")
        click(285, 109)
        click(63, 255)
        click(120, 276)
        click(360, 190)
        key("Return")
        capture("sequence-definition-1280")
        cap.resize(window, 840, 560)
        capture("sequence-definition-840")
        after = psql("SELECT last_value,is_called,(SELECT count(*) FROM browse.effects) FROM browse.counter")
        if before != "9223372036854775805|f|0" or before != after:
            raise RuntimeError("Read-only browsing advanced sequence or executed a routine")
        if source_snapshot() != source:
            raise RuntimeError("Source changed during formal capture")
        (evidence / "result.json").write_text(json.dumps(dict(binary_sha256=digest, before=before, after=after,
            read_only_unchanged=True, original_comparison=False, visual_review_required=True), indent=2) + "\n")
    except BaseException:
        if cap is not None and window is not None:
            try:
                capture("FAILED-last-state")
            except BaseException as capture_error:
                print("Failure screenshot unavailable:", capture_error, file=sys.stderr)
        raise
    finally:
        failure = sys.exc_info()[1]
        try:
            cleanup_resources(root, state, cap, container, evidence)
        except BaseException as cleanup_error:
            if failure is not None:
                raise BaseExceptionGroup("Capture and cleanup failed", [failure, cleanup_error]) from None
            raise


if __name__ == "__main__":
    main()
