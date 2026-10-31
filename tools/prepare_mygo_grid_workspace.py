#!/usr/bin/env python3
"""Build the reviewed local MyGo candidate without publishing a module tag."""

import argparse
import os
from pathlib import Path
import shlex
import subprocess


BASE = "922da57bd8b2ec6cf1576441afbe7f6565a536ca"
REMOTE = "https://github.com/nyakang/mygo.git"


def run(*args, cwd=None, capture=False):
    return subprocess.run(args, cwd=cwd, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None)


def main():
    root = Path(__file__).resolve().parents[1]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path,
                        default=root / "temp/mygo-grid-workspace")
    parser.add_argument("--source", default=REMOTE,
                        help="Git repository to clone (a local clone also works)")
    args = parser.parse_args()
    directory = args.directory.resolve()
    if directory.exists():
        parser.error(f"refusing to overwrite existing workspace: {directory}")
    patch = root / "docs/patches/mygo-v0.7.3-nyadb.2.patch"
    if not patch.is_file():
        parser.error(f"missing reviewed patch: {patch}")
    directory.mkdir(parents=True)
    fork = directory / "fork"
    run("git", "clone", "--no-checkout", args.source, str(fork))
    run("git", "checkout", "--detach", BASE, cwd=fork)
    actual = run("git", "rev-parse", "HEAD", cwd=fork, capture=True).stdout.strip()
    if actual != BASE:
        raise RuntimeError(f"unexpected MyGo baseline: {actual}")
    run("git", "apply", "--check", str(patch), cwd=fork)
    run("git", "apply", str(patch), cwd=fork)
    environment = dict(os.environ, GOWORK="off")
    subprocess.run(["go", "work", "init", str(root)],
                   cwd=directory, env=environment, check=True)
    subprocess.run(["go", "work", "edit", f"-replace=github.com/egoist/mygo={fork}",
                    str(directory / "go.work")],
                   cwd=directory, env=environment, check=True)
    print(f"Local v0.7.3-nyadb.2 candidate ready; no tag or commit was created.")
    print(f"GOWORK={shlex.quote(str(directory / 'go.work'))} CGO_ENABLED=0 go test ./...")


if __name__ == "__main__":
    main()
