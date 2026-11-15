#!/usr/bin/env python3
"""Verify TDengine protocol semantics with an owned official 3.3.6.13 fixture.

This gate verifies native protocol foundations only. It does not publish a
NyaDB connection type or establish full product/workbench acceptance.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
import subprocess
import sys
import time

ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / "tools/tdengine_fixture_probe"
IMAGE = "tdengine/tdengine@sha256:79d32ce8a42f64c7dbfc33946a36b3e176ea23c0cd695a1bc9e8aa889ca03b88"
VERSION = "3.3.6.13"
REQUIRED_TESTS = {
    "TestNativeTDengineVersionAuthenticationAndPhysicalIsolation",
    "TestNativeTDengineTablesStablesChildrenAndCompleteSources",
    "TestNativeTDengineExactTypesNullTextBinaryAndTimestamp",
    "TestNativeTDengineOriginalCursorPagingAndClose",
    "TestNativeTDengineInsertUpdateModesAndUnsupportedUpdate",
    "TestNativeTDengineCompositeKeysAndDeleteScope",
    "TestNativeTDengineSequentialBatchIsNotAtomic",
    "TestNativeTDengineLostOriginalWriteAcknowledgementDoesNotReconnect",
}
APPLICATION_TESTS = {
	"TestNativeTDengineDataWriteExactMergeNullEmptyAndDelete",
	"TestNativeTDengineDataWriteStableOriginalChildAndRevokedMetadata",
	"TestNativeTDengineDataWritePartialFailureRetainsOriginalPrefix",
	"TestNativeTDengineDataWriteBatchPreviewBudgetAndDuplicateKeys",
	"TestNativeTDengineDataWriteLostAcknowledgementCloseAndSiblingIsolation",
	"TestNativeTDengineDataDocumentCompleteRowsAndOriginalIdentity",
	"TestNativeTDengineDataDocumentStableChildAndMetadataRevocation",
	"TestNativeTDengineDataDocumentCloseWaitsAndSiblingReadsRemainIndependent",
	"TestNativeTDengineFieldProjectionSemantics",
    "TestNativeTDengineOriginalTypesMetadataPagingAndFullExport",
    "TestNativeTDengineStableChildHierarchyAndCompositeIdentity",
    "TestNativeTDengineSequentialFailureReceiptsAndPhysicalIsolation",
    "TestNativeTDengineReadOnlyCredentialsAndClosedGeneration",
    "TestNativeTDengineCancelledOriginalWriteReceiptAndExplicitRecovery",
    "TestTDengineNativePublicChooserVaultSQLGridStructureAndRecovery",
}


def passed_tests(log):
    found = set()
    for line in log.splitlines():
        try:
            event = json.loads(line)
        except ValueError:
            continue
        if event.get("Action") == "pass" and event.get("Test"):
            found.add(event["Test"])
    return found


def owned_container(state, original_id, nonce):
    return (
        state.get("Id") == original_id
        and state.get("Config", {}).get("Labels", {}).get("nyadb.owner") == nonce
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--application", action="store_true", help="also verify the production provider and public workbench")
    args = parser.parse_args()
    references = [
        ROOT / "temp/dbx/plugins/connection-types/tdengine.yaml",
        ROOT / "temp/dbx/agents/drivers/tdengine/Cargo.toml",
        ROOT / "temp/dbx/agents/drivers/tdengine/src/config.rs",
        ROOT / "temp/dbx/agents/drivers/tdengine/src/driver.rs",
        ROOT / "temp/dbx/agents/drivers/tdengine/src/value.rs",
        ROOT / "temp/dbx/crates/dbx-sql-data/src/data_grid_tdengine_sql.rs",
    ]
    for reference in references:
        if not reference.is_file():
            parser.error("local DBX reference required: " + str(reference))
    nonce = secrets.token_hex(8)
    folder = ROOT / "build/tdengine" / ("protocol-" + nonce)
    folder.mkdir(parents=True)
    private = ROOT / "temp" / ("tdengine-fixture-" + nonce)
    private.mkdir(mode=0o700)
    # TDengine's default strong-password policy requires several character
    # classes and caps the password at 32 bytes.
    password = "Nya1!" + secrets.token_hex(8)
    secret = private / "password"
    secret.write_text(password)
    secret.chmod(0o600)
    envfile = private / "server.env"
    envfile.write_text(
        "TAOS_ROOT_PASSWORD=" + password + "\n"
        "TAOS_DISABLE_KEEPER=1\nTAOS_DISABLE_EXPLORER=1\n"
    )
    envfile.chmod(0o600)
    server_config = private / "taos.cfg"
    server_config.write_text(
        "fqdn localhost\nfirstEp localhost:6030\nserverPort 6030\n"
        "timezone UTC\nnumOfRpcThreads 2\nnumOfTaskQueueThreads 4\n"
        "numOfCommitThreads 2\nsupportVnodes 16\ntelemetryReporting 0\nmonitor 0\n"
    )
    sources = [Path(__file__), *MODULE.glob("*.go"), MODULE / "go.mod", MODULE / "go.sum"]
    if args.application:
        sources.extend(ROOT.glob("go.*"))
        sources.extend(ROOT.glob("locales/*"))
        sources.extend(p for p in (ROOT / "internal").rglob("*") if p.is_file())
    result = dict(
        status="running", runner_pid=os.getpid(), image=IMAGE,
        native_version=VERSION, checks=[], goal_complete=False,
        public_provider_published=False, protocol_foundation_only=True,
        source_sha256={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources},
        reference_sha256={str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in references},
    )
    if args.application:
        result.update(public_provider_published=True, protocol_foundation_only=False, full_product_alignment=False)
    original = None
    environment = {k: v for k, v in os.environ.items() if not (k.startswith("NYADB_") and k.endswith("_IT"))}
    environment.update(GOWORK="off", CGO_ENABLED="1" if args.race else "0")

    def save():
        (folder / "result.json").write_text(json.dumps(result, indent=2) + "\n")

    def frozen():
        for field in ["source_sha256", "reference_sha256"]:
            for path, digest in result[field].items():
                if hashlib.sha256((ROOT / path).read_bytes()).hexdigest() != digest:
                    raise RuntimeError("accepted source changed: " + path)

    def run(name, command, env=None, required=None, cwd=MODULE):
        frozen()
        start = time.monotonic()
        logpath = folder / (name + ".log")
        with logpath.open("w") as log:
            process = subprocess.Popen(command, cwd=cwd, env=env or environment, stdout=log, stderr=subprocess.STDOUT)
            check = dict(name=name, command=command, original_pid=process.pid, status="running")
            result["checks"].append(check)
            save()
            print("Original TDengine gate " + name + " pid=" + str(process.pid), flush=True)
            code = process.wait()
        text = logpath.read_text()
        found = passed_tests(text)
        missing = sorted((required or set()) - found)
        check.update(exit_code=code, seconds=round(time.monotonic() - start, 2), passed_tests=sorted(found), missing_tests=missing, status="passed" if code == 0 and not missing else "failed")
        save()
        frozen()
        if code or missing:
            raise RuntimeError("original " + name + " gate failed; inspect " + str(logpath))

    save()
    print("TDengine protocol evidence=" + str(folder) + " runner=" + str(os.getpid()), flush=True)
    try:
        run("module-integrity", ["go", "mod", "verify"])
        module = subprocess.check_output(["go", "mod", "download", "-json", "github.com/taosdata/driver-go/v3@v3.8.2"], cwd=MODULE, env=environment, text=True)
        provenance = json.loads(module)
        if provenance.get("Sum") != "h1:PcbXvNYHgLZde3MgxtE7hsrn7vIak13tMloiHFf75mU=":
            raise RuntimeError("pinned connector module checksum mismatch")
        (folder / "connector-module.json").write_text(module)
        binary = folder / "tdengine-ready"
        run("ready-build", ["go", "build", "-mod=readonly", "-o", str(binary), "."], dict(environment, CGO_ENABLED="0"))
        image = json.loads(subprocess.check_output(["docker", "image", "inspect", IMAGE], text=True))[0]
        (folder / "official-image.json").write_text(json.dumps({k: image[k] for k in ["Id", "RepoDigests", "Architecture"]}, indent=2) + "\n")
        original = subprocess.check_output([
            "docker", "run", "-d", "--name", "nyadb-tdengine-protocol-" + nonce,
            "--label", "nyadb.owner=" + nonce, "--memory", "2g", "--cpus", "2",
            "--env-file", str(envfile), "-v", str(server_config) + ":/etc/taos/taos.cfg:ro",
            "-p", "127.0.0.1::6041", IMAGE,
        ], text=True).strip()
        result["original_container"] = original
        save()
        state = json.loads(subprocess.check_output(["docker", "inspect", original], text=True))[0]
        if not owned_container(state, original, nonce):
            raise RuntimeError("fixture identity changed")
        port = state["NetworkSettings"]["Ports"]["6041/tcp"][0]["HostPort"]
        config = private / "config.json"
        config.write_text(json.dumps(dict(Endpoint="ws://127.0.0.1:" + port + "/ws", Host="127.0.0.1", Port=int(port), Image=IMAGE, Container=original, Username="root", PasswordFile=str(secret), DatabasePrefix="nyadb_td_" + nonce, Version=VERSION)))
        config.chmod(0o600)
        deadline = time.monotonic() + 90
        while True:
            ready = subprocess.run([str(binary), "-config", str(config)], capture_output=True, text=True, timeout=8)
            if ready.returncode == 0:
                (folder / "native-version.json").write_text(ready.stdout)
                break
            state = json.loads(subprocess.check_output(["docker", "inspect", original], text=True))[0]
            if not owned_container(state, original, nonce) or not state["State"]["Running"]:
                raise RuntimeError("original owned fixture exited during readiness")
            if time.monotonic() >= deadline:
                raise RuntimeError("original owned fixture readiness failed")
            time.sleep(0.25)
        native_env = dict(environment, NYADB_TDENGINE_PROTOCOL_IT="1", NYADB_TDENGINE_PROTOCOL_CONFIG=str(config), NYADB_TDENGINE_PROTOCOL_EVIDENCE=str(folder / "native-evidence"))
        command = ["go", "test", "-mod=readonly", "-count=1", "-timeout=10m", "-json"]
        if args.race:
            command.append("-race")
        run("native-protocol", command + ["./..."], native_env, REQUIRED_TESTS)
        if args.application:
            app_env = dict(environment, NYADB_TDENGINE_IT="1", NYADB_TDENGINE_CONFIG=str(config), NYADB_TDENGINE_EVIDENCE=str(folder / "application-evidence"), NYADB_TDENGINE_SHELL_EVIDENCE=str(folder / "workbench-evidence"))
            app_command = ["go", "test", "-mod=readonly", "-count=1", "-timeout=12m", "-json", "-run", "TDengine"]
            if args.race:
                app_command.append("-race")
            run("native-application", app_command + ["./internal/services/tdengine", "./internal/ui/shell"], app_env, APPLICATION_TESTS, ROOT)
        run("go-vet", ["go", "vet", "-mod=readonly", "./..."])
        for name, target_os, arch in [("linux", "linux", "amd64"), ("windows", "windows", "amd64"), ("macos", "darwin", "arm64")]:
            run("build-" + name, ["go", "build", "-mod=readonly", "-o", str(folder / ("probe-" + name)), "."], dict(environment, CGO_ENABLED="0", GOOS=target_os, GOARCH=arch))
        result["status"] = "passed"
    except Exception as error:
        result.update(status="failed", error=str(error).replace(password, "[redacted]"))
        print(result["error"], file=sys.stderr, flush=True)
    finally:
        try:
            if original:
                state = json.loads(subprocess.check_output(["docker", "inspect", original], text=True))[0]
                if not owned_container(state, original, nonce):
                    raise RuntimeError("refusing to remove a changed fixture identity")
                logs = subprocess.run(["docker", "logs", original], capture_output=True, text=True)
                redacted = (logs.stdout + logs.stderr).replace(password, "[redacted]")
                redacted = re.sub(r"input:[^,\s]+", "input:[redacted]", redacted)
                (folder / "server.log").write_text(redacted)
                subprocess.run(["docker", "rm", "-f", "-v", original], check=True, capture_output=True)
                result["original_container_removed"] = subprocess.run(["docker", "inspect", original], capture_output=True).returncode != 0
        except Exception as error:
            result.update(status="failed", cleanup_error=str(error).replace(password, "[redacted]"))
        shutil.rmtree(private)
        result["private_credentials_removed"] = not private.exists()
        save()
    print(json.dumps({k: v for k, v in result.items() if k not in ["source_sha256", "reference_sha256"]}), flush=True)
    return 0 if result["status"] == "passed" else 1


if __name__ == "__main__":
    sys.exit(main())
