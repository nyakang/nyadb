#!/usr/bin/env python3
"""Install the verified DuckDB library in NyaDB's cache or a package directory."""
import argparse
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import platform
import tempfile
import time
import urllib.error
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
MANIFEST = json.loads((ROOT / "resources/duckdb/runtime_manifest.json").read_text())
DOWNLOAD_ATTEMPTS = 4
RETRYABLE_HTTP_STATUS = {408, 429, 500, 502, 503, 504}

def download_archive(url):
    request = urllib.request.Request(url, headers={"User-Agent": "NyaDB-DuckDB-runtime"})
    for attempt in range(DOWNLOAD_ATTEMPTS):
        try:
            with urllib.request.urlopen(request, timeout=60) as response:
                return response.read(100 * 1024 * 1024 + 1)
        except urllib.error.HTTPError as error:
            error.close()
            if error.code not in RETRYABLE_HTTP_STATUS or attempt == DOWNLOAD_ATTEMPTS - 1:
                raise
        except (urllib.error.URLError, TimeoutError, ConnectionError, http.client.IncompleteRead):
            if attempt == DOWNLOAD_ATTEMPTS - 1:
                raise
        delay = 2 ** attempt
        print(f"DuckDB download attempt {attempt + 1} failed; retrying in {delay}s", flush=True)
        time.sleep(delay)

def host_target():
    system = {"Linux": "linux", "Darwin": "darwin", "Windows": "windows"}.get(platform.system())
    arch = {"x86_64": "amd64", "AMD64": "amd64", "aarch64": "arm64", "arm64": "arm64", "ARM64": "arm64"}.get(platform.machine())
    return f"{system}-{arch}"

def cache_root():
    if platform.system() == "Windows":
        return Path(os.environ["LOCALAPPDATA"])
    if platform.system() == "Darwin":
        return Path.home() / "Library/Caches"
    return Path(os.environ.get("XDG_CACHE_HOME", Path.home() / ".cache"))

def atomic_write(path, data):
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as f:
        temporary = Path(f.name)
        try:
            f.write(data)
            f.flush()
            os.fsync(f.fileno())
        except BaseException:
            temporary.unlink(missing_ok=True)
            raise
    try:
        temporary.replace(path)
    finally:
        temporary.unlink(missing_ok=True)

def install(target, output):
    entry = MANIFEST["archives"][target]
    version = MANIFEST["version"]
    output = Path(output) if output else cache_root() / "NyaDB/duckdb" / version / target
    output.mkdir(parents=True, exist_ok=True)
    suffix = {"linux": ".so", "darwin": ".dylib", "windows": ".dll"}[target.split("-")[0]]
    filename = "duckdb.dll" if suffix == ".dll" else "libduckdb" + suffix
    path = output / filename
    url = f"https://github.com/duckdb/duckdb/releases/download/{version}/{entry['name']}"
    if path.is_file() and path.stat().st_size <= 256 * 1024 * 1024 and hashlib.sha256(path.read_bytes()).hexdigest() == entry["library_sha256"]:
        atomic_write(output / "DuckDB-LICENSE.txt", (ROOT / "resources/duckdb/LICENSE.txt").read_bytes())
        print(f"{target}: verified existing {path.resolve()}", flush=True)
        return path
    archive = download_archive(url)
    if len(archive) > 100 * 1024 * 1024 or hashlib.sha256(archive).hexdigest() != entry["sha256"]:
        raise RuntimeError("DuckDB archive integrity check failed")
    with zipfile.ZipFile(io.BytesIO(archive)) as z:
        names = [n for n in z.namelist() if Path(n).name in ("libduckdb" + suffix, "duckdb" + suffix)]
        if len(names) != 1 or z.getinfo(names[0]).file_size > 256 * 1024 * 1024:
            raise RuntimeError("Unexpected DuckDB library archive")
        library = z.read(names[0])
    digest = hashlib.sha256(library).hexdigest()
    if digest != entry["library_sha256"]:
        raise RuntimeError("DuckDB library integrity check failed")
    atomic_write(path, library)
    atomic_write(output / "DuckDB-LICENSE.txt", (ROOT / "resources/duckdb/LICENSE.txt").read_bytes())
    record = {"version": version, "target": target, "source": url, "archive_sha256": entry["sha256"], "library_sha256": digest, "license": "MIT"}
    atomic_write(output / "duckdb-runtime.json", (json.dumps(record, indent=2) + "\n").encode())
    print(f"{target}: {path.resolve()} (SHA256 {digest})", flush=True)
    return path

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--target", choices=MANIFEST["archives"], default=host_target())
    parser.add_argument("--output", help="output directory; default is NyaDB's versioned user cache")
    args = parser.parse_args()
    if args.target not in MANIFEST["archives"]:
        parser.error("unsupported platform")
    if args.target != host_target() and not args.output:
        parser.error("cross-platform installations require --output")
    install(args.target, args.output)

if __name__ == "__main__":
    main()
