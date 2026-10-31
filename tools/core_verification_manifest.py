#!/usr/bin/env python3
"""Bind explicit verification receipts to the current worktree and artifacts.

This records results from the caller; it does not run checks or turn a missing
desktop/differential observation into a pass. Docs are excluded from the source
digest so the report can reference this manifest without a circular hash.
"""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
from datetime import datetime, timezone


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--receipts", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    receipts = json.loads(args.receipts.read_text(encoding="utf-8"))
    names = subprocess.check_output(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], cwd=root
    ).decode().split("\0")
    files = []
    digest = hashlib.sha256()
    for name in sorted(set(names)):
        if not name or name.startswith(("docs/", "build/", "temp/")):
            continue
        path = root / name
        if not path.is_file():
            continue
        file_hash = sha256(path)
        files.append({"path": name, "bytes": path.stat().st_size, "sha256": file_hash})
        digest.update(name.encode() + b"\0" + file_hash.encode() + b"\n")
    artifacts = []
    for name in receipts.pop("artifact_paths"):
        path = root / name
        artifacts.append({"path": name, "bytes": path.stat().st_size, "sha256": sha256(path)})
    for check in receipts["checks"]:
        path = root / check["log"]
        check["log_sha256"] = sha256(path)
    for evidence in receipts.get("native_evidence", []):
        evidence["sha256"] = sha256(root / evidence["path"])
    payload = {
        "format": "nyadb-core-verification", "version": 1,
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "git_head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root).decode().strip(),
        "uncommitted": bool(subprocess.check_output(["git", "status", "--porcelain"], cwd=root)),
        "source": {"algorithm": "SHA-256(path + NUL + file SHA-256 + LF), sorted paths",
                   "excluded": ["docs/", "build/", "temp/", "gitignored files"],
                   "sha256": digest.hexdigest(), "files": files},
        "dependencies": {name: sha256(root / name) for name in ("go.mod", "go.sum")},
        "artifacts": artifacts, **receipts,
    }
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"source_sha256": digest.hexdigest(), "source_files": len(files),
                      "artifacts": len(artifacts)}, indent=2))


if __name__ == "__main__":
    main()
