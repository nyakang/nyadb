#!/usr/bin/env python3
"""Build NyaDB with its verified DuckDB runtime as a portable directory."""
import argparse
import os
from pathlib import Path
import subprocess

from install_duckdb_runtime import MANIFEST, ROOT, host_target, install


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=MANIFEST["archives"], default=host_target())
    parser.add_argument("--output", help="directory; defaults to build/duckdb/package-<target>")
    args = parser.parse_args()
    if args.target not in MANIFEST["archives"]:
        parser.error("unsupported platform")
    output = Path(args.output) if args.output else ROOT / "build/duckdb" / ("package-" + args.target)
    output = output.resolve()
    install(args.target, output)
    goos, goarch = args.target.split("-")
    executable = output / ("nyadb.exe" if goos == "windows" else "nyadb")
    env = dict(os.environ, GOOS=goos, GOARCH=goarch, CGO_ENABLED="0")
    subprocess.run(["go", "build", "-o", str(executable), "./cmd/nyadb"], cwd=ROOT, env=env, check=True)
    print(f"Built {executable}. Distribute the complete directory including DuckDB-LICENSE.txt.")


if __name__ == "__main__":
    main()
