#!/usr/bin/env python3
"""Verify DuckDB native, adapter and shared-grid fixtures without user data."""
import argparse
import json
import os
from pathlib import Path
import subprocess

from install_duckdb_runtime import ROOT, host_target, install


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", default=str(ROOT / "build/duckdb/verification"))
    parser.add_argument("--race", action="store_true", help="requires a host C compiler for Go's race detector")
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    library = install(host_target(), output / "runtime")
    env = dict(os.environ, CGO_ENABLED="1" if args.race else "0", NYADB_DUCKDB_IT="1", NYADB_DUCKDB_LIBRARY=str(library), NYADB_DUCKDB_VISUAL_DIR=str(output))
    command = ["go", "test", "-count=1", "-timeout=20m"]
    if args.race:
        command.append("-race")
    command.extend(["./internal/services/duckdb/...", "./internal/ui/shell", "-run", "DuckDB"])
    with (output / ("race.log" if args.race else "fixtures.log")).open("w") as log:
        result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT)
    report = {"target": host_target(), "version": "v1.4.4", "race": args.race, "passed": result.returncode == 0, "fixture": "testing.T.TempDir files; no user database or credentials", "command": command}
    (output / "verification.json").write_text(json.dumps(report, indent=2) + "\n")
    if result.returncode:
        raise SystemExit(f"DuckDB fixture failed; see {output}")
    print(f"DuckDB fixture passed; logs, report and grid capture: {output}")


if __name__ == "__main__":
    main()
