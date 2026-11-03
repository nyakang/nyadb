#!/usr/bin/env python3
"""Capture formal PostgreSQL table design saving in an owned Linux run.

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


from capture_postgres_program_acceptance import source_snapshot, cleanup_resources


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--explore", action="store_true")
    parser.add_argument("--explore-new", action="store_true")
    parser.add_argument("--scenario", choices=("edit", "new", "failure"), default="edit")
    opts = parser.parse_args()
    root, state = validate(Path(os.environ["NYADB_ENTRY_ROOT"]))
    if "app" in state["processes"] or (root / "config/NyaDB/config.sqlite").exists():
        raise RuntimeError("PostgreSQL designer capture requires a fresh owned config and process")
    env = environment(root, state)
    evidence = root / "evidence/postgres-designer-native" / ("capture-" + uuid.uuid4().hex[:12])
    evidence.mkdir(parents=True)
    name = "nyadb-designer-" + state["id"]
    label = "org.nyadb.program-fixture"
    container = None
    cap = None
    window = None
    binary = REPO / "build/linux-amd64/nyadb"
    digest = hashlib.sha256(binary.read_bytes()).hexdigest()
    source = source_snapshot()
    (evidence / "source-check.json").write_text(json.dumps(source, indent=2) + "\n")

    def capture(name):
        cap.capture(name, window, "postgres-designer", dict(binary_sha256=digest,
            real_postgres=True, schema="design", original_comparison=False, scale="unknown", loading=False,
            source_tree_sha256=source["tree_sha256"], visual_review_required=True))

    def capture_pair(name):
        capture(name + "-1280")
        cap.resize(window, 840, 560)
        time.sleep(.4)
        capture(name + "-840")
        cap.resize(window, 1280, 800)
        time.sleep(.4)

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
            "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "postgres:18-alpine"])
        (evidence / "container.json").write_text(json.dumps(dict(id=container, name=name, label=label, run_id=state["id"]), indent=2) + "\n")
        for _ in range(60):
            ready = subprocess.run(["docker", "exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "nyadb_it", "-d", "nyadb_it"], capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("Owned PostgreSQL fixture did not become ready")
        env["NYADB_DESIGN_PORT"] = command(["docker", "port", container, "5432/tcp"]).rsplit(":", 1)[1]
        psql("""CREATE SCHEMA design;
CREATE TABLE design.shared(id bigint PRIMARY KEY, label text NOT NULL, amount numeric(40,20), payload bytea, optional text, items integer[]);
INSERT INTO design.shared VALUES(1,'retained fixture',12345678901234567890.12345678901234567890,decode('00ff','hex'),NULL,ARRAY[1,2]);
""")
        before = psql("SELECT id,label,amount::text,encode(payload,'hex'),optional IS NULL,items::text FROM design.shared")
        subprocess.run(["go", "run", "./tools/entryfixture", "seed-postgres-designer"], cwd=REPO, env=env, check=True)
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
        creating = opts.explore_new or opts.scenario == "new"
        table = "native_created" if creating else "shared"
        field = "native_failed" if opts.scenario == "failure" else "native_added"
        if creating:
            click(360, 190)
            key("ctrl+n")
            capture_pair("new-table-confirmation")
            if opts.explore_new:
                return
            cap.run("xdotool", "type", "--clearmodifiers", "--", table)
            click(830, 480)
        else:
            click(360, 190)
            click(355, 137)
            time.sleep(1)
            capture_pair("structure-readonly")
            click(713, 137)
        time.sleep(1)
        capture_pair("designer-fields")
        if opts.explore:
            return
        click(292, 211)
        click(420, 441)
        key("ctrl+a")
        cap.run("xdotool", "type", "--clearmodifiers", "--", field)
        capture_pair("designer-add-draft")
        key("ctrl+s")
        capture_pair("designer-preview")
        if psql("SELECT count(*) FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='design' AND c.relname='" + table + "' AND a.attname='" + field + "'") != "0":
            raise RuntimeError("Preview executed without confirmation")
        if opts.scenario == "failure":
            # Change the captured revision externally after preview. The
            # executor must reject the stale draft under the original lock.
            psql("ALTER TABLE design.shared ADD COLUMN concurrent_marker integer")
        click(954, 563)
        if opts.scenario == "failure":
            time.sleep(5)
            capture_pair("designer-failed-draft-retained")
            if psql("SELECT count(*) FROM pg_attribute WHERE attrelid='design.shared'::regclass AND attname='native_failed'") != "0":
                raise RuntimeError("Rejected draft changed the table")
        else:
            deadline = time.monotonic() + 60
            while psql("SELECT count(*) FROM pg_attribute WHERE attrelid=to_regclass('design." + table + "') AND attname='" + field + "'") != "1":
                if time.monotonic() > deadline:
                    raise RuntimeError("Formal native save did not add the intended column")
                time.sleep(.2)
            time.sleep(1)
            capture_pair("designer-applied")
            # Show the committed added field, including in the shorter list
            # viewport. Scrolling is recorded as an ordinary native action.
            for _ in range(3):
                cap.actions.append(dict(action="scroll", client_x=450, client_y=280, button=5))
                cap.click(window, 450, 280, button=5)
            time.sleep(.4)
            capture("designer-applied-new-field-1280")
            cap.resize(window, 840, 560)
            for _ in range(3):
                cap.actions.append(dict(action="scroll", client_x=450, client_y=300, button=5))
                cap.click(window, 450, 300, button=5)
                time.sleep(.2)
            time.sleep(.4)
            capture("designer-applied-new-field-840")
            cap.resize(window, 1280, 800)
        after = psql("SELECT id,label,amount::text,encode(payload,'hex'),optional IS NULL,items::text FROM design.shared")
        if before != after:
            raise RuntimeError("Native design changed retained typed values")
        if opts.scenario == "edit" and psql("SELECT native_added IS NULL FROM design.shared") != "t":
            raise RuntimeError("Native design did not retain the new NULL projection")
        catalog = psql("SELECT c.relname,a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid WHERE n.nspname='design' AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped ORDER BY c.relname,a.attnum")
        if source_snapshot() != source:
            raise RuntimeError("Source changed during formal capture")
        (evidence / "result.json").write_text(json.dumps(dict(binary_sha256=digest, before=before, after=after,
            scenario=opts.scenario, catalog=catalog, retained_values_verified=True,
            original_comparison=False, visual_review_required=True), indent=2) + "\n")
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
