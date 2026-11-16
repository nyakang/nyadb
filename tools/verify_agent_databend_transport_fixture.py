#!/usr/bin/env python3
"""Diagnose original Databend JDBC transport against an owned real product.

This is a separately gated local original-agent diagnostic. It does not accept
managed packaging, a public provider/card, cloud routing, data editing, or
workspaces. The original-package input must have an unchanged complete manifest
and original-source/dependency provenance; it is not imported into driverstore.
"""
import argparse
import base64
import hashlib
import json
from decimal import Decimal
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'datafuselabs/databend@sha256:7bcd856fd91583ea05de61bbe0b60c7fe136695538ddc07f2823cdae70c064e9'
JDBC_SHA = '274597a199f205b347e66db63b384e1846e5982b759c4d0f31abd5e74e82e2d3'
REQUIRED = [
    'TestDatabendTransportLocalOriginalValuesCursorAndNativeStructure',
    'TestDatabendTransportLocalOriginalDriverRetriesOnlyCachedReceipt',
    'TestDatabendTransportLocalCommittedUpstreamReplyLossNeverReplays',
    'TestDatabendTransportLocalOriginalTLSGatewayAndAuthentication',
    'TestDatabendTransportLocalOriginalQueuedCancellationAndIndependentSession',
    'TestDatabendTransportLocalOriginalUnsignedBoundariesAndCompositeValues',
    'TestDatabendTransportLocalOriginalActiveCancellationAcknowledgement',
    'TestDatabendTransportLocalOriginalKeylessGuardsAndPartialBatch',
]


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def run(args):
    return subprocess.check_output(args, stderr=subprocess.STDOUT)


def verify_named_acceptance(output):
    for name in REQUIRED:
        if not re.search(r'^\s*--- PASS: ' + re.escape(name) + r' \(', output, re.MULTILINE):
            raise RuntimeError('Required original transport diagnostic missing: ' + name)
    for name in ['untrusted-certificate', 'wrong-hostname', 'wrong-password']:
        case = 'TestDatabendTransportLocalOriginalTLSGatewayAndAuthentication/' + name
        if not re.search(r'^\s*--- PASS: ' + re.escape(case) + r' \(', output, re.MULTILINE):
            raise RuntimeError('Required original TLS/authentication diagnostic missing: ' + name)


def source_snapshot(dbx):
    files = []
    for relative in ['agents/common/src', 'agents/drivers/databend/src']:
        files += [p for p in (dbx / relative).rglob('*') if p.is_file()]
    for relative in ['LICENSE', 'NOTICE', 'agents/build.gradle', 'agents/common/build.gradle',
                     'agents/drivers/databend/build.gradle', 'agents/versions.json',
                     'plugins/connection-types/databend.yaml', 'plugins/connection-types/profiles/catalog.yaml']:
        files.append(dbx / relative)
    return {p.relative_to(dbx).as_posix(): sha(p.read_bytes()) for p in sorted(files)}


def verify_original_package(package, dbx):
    manifest = json.loads((package / 'manifest.json').read_text())
    if (manifest['key'], manifest['runtime'], manifest['main_class']) != (
            'databend', 'java', 'com.dbx.agent.databend.DatabendAgent'):
        raise RuntimeError('Original Databend process identity differs')
    declared = set()
    for item in manifest['files']:
        path = package / item['path']
        if path.resolve().is_relative_to(package.resolve()) is False or item['path'] in declared:
            raise RuntimeError('Original artifact identity is ambiguous')
        declared.add(item['path'])
        if sha(path.read_bytes()) != item['sha256']:
            raise RuntimeError('Original package artifact changed')
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != declared | {'manifest.json'}:
        raise RuntimeError('Original package has undeclared or missing files')
    if sha((package / 'databend-jdbc-0.4.8.jar').read_bytes()) != JDBC_SHA:
        raise RuntimeError('Original Databend JDBC fixed pin differs')
    provenance = json.loads((package / manifest['provenance']).read_text())
    expected_sources = sorted((dbx / 'agents/common/src/main/java').rglob('*.java'))
    expected_sources += sorted((dbx / 'agents/drivers/databend/src/main/java').rglob('*.java'))
    expected = {p.relative_to(dbx).as_posix(): sha(p.read_bytes()) for p in expected_sources}
    recorded = {item['path']: item['sha256'] for item in provenance['source_files']}
    if expected != recorded:
        raise RuntimeError('Unmodified original source provenance differs')
    if provenance['agent_sha256'] != sha((package / manifest['classpath'][0]).read_bytes()):
        raise RuntimeError('Original compiled agent provenance differs')
    for item in provenance['dependencies']:
        if sha(Path(item['file']).read_bytes()) != item['sha256']:
            raise RuntimeError('Original dependency provenance changed')
    return {'manifest_sha256': sha((package / 'manifest.json').read_bytes()),
            'provenance_sha256': sha((package / manifest['provenance']).read_bytes()),
            'jdbc_sha256': JDBC_SHA, 'original_source_files': len(expected),
            'artifact_files': len(declared)}


def verify_receipt(record):
    raw = base64.b64decode(record['native_bytes'], validate=True)
    receipt = record['receipt']
    if sha(raw) != record['native_sha256'] or receipt.get('ExecutorAcknowledged') is not True or receipt.get('Dispatched') is not True:
        raise RuntimeError('Original receipt bytes/acknowledgement differ')
    envelope = json.loads(raw, parse_float=Decimal)
    if envelope.get('jsonrpc') != '2.0' or not isinstance(envelope.get('id'), int) or envelope['id'] <= 0 or ('result' in envelope) == ('error' in envelope):
        raise RuntimeError('Original complete v2 envelope is missing')
    if 'result' in envelope:
        if envelope['result'] != receipt.get('Result') or receipt.get('RemoteError') is not None:
            raise RuntimeError('Original typed result differs from its complete envelope')
    elif not receipt.get('RemoteError') or envelope['error'].get('code') != receipt['RemoteError'].get('Code') or receipt.get('Result') is not None:
        raise RuntimeError('Original error differs from its complete envelope')


def verify_receipts(directory):
    receipts = [json.loads(line, parse_float=Decimal) for line in (directory / 'original-receipts.jsonl').read_text().splitlines()]
    if not receipts:
        raise RuntimeError('Original executor receipts are missing')
    covered = set()
    for record in receipts:
        verify_receipt(record)
        covered.add(record['test'].split('/')[0])
    if set(REQUIRED) - covered:
        raise RuntimeError('A named diagnostic has no original executor evidence')
    return len(receipts)


def verify_active_cancellation(directory):
    evidence = json.loads((directory / 'active-cancellation.json').read_text())
    if (not re.fullmatch('[0-9a-f]{32}', evidence.get('original_query_id', '')) or
            evidence.get('initial_state') not in ['Starting', 'Running'] or
            evidence.get('first_page_observed') is not True or
            evidence.get('original_final_http_status') != 200 or
            evidence.get('upstream_submissions') != 1 or
            evidence.get('executor_acknowledged') is not True or
            evidence.get('native_kill_or_rollback_accepted') is not False):
        raise RuntimeError('Captured original active cancellation scope/acknowledgement differs')
    records = [json.loads(line, parse_float=Decimal) for line in (directory / 'original-receipts.jsonl').read_text().splitlines()]
    canceled = [r for r in records if r['method'] == 'active-canceled-original']
    if len(canceled) != 1:
        raise RuntimeError('Original active executor receipt is missing or ambiguous')
    verify_receipt(canceled[0])
    receipt = canceled[0]['receipt']
    if receipt.get('CancellationRequested') is not True or receipt.get('ProcessExitConfirmed') is not False:
        raise RuntimeError('Original executor cancellation was substituted with process termination')
    return evidence


def verify_mutation_evidence(directory):
    records = [json.loads(line, parse_float=Decimal) for line in (directory / 'original-receipts.jsonl').read_text().splitlines()]
    counts = []
    partial = []
    for record in records:
        if record['test'] != 'TestDatabendTransportLocalOriginalKeylessGuardsAndPartialBatch':
            continue
        if record['method'] == 'execute_query':
            verify_receipt(record)
            result = record['receipt']['Result']
            if (result.get('column_types') != ['uint64'] or result.get('truncated') is not False or
                    result.get('affected_rows') != 0 or len(result.get('rows', [])) != 1 or
                    len(result['rows'][0]) != 1 or type(result['rows'][0][0]) is not int):
                raise RuntimeError('Original native mutation count shape changed')
            counts.append((result.get('columns'), result['rows'][0][0]))
        elif record['method'] == 'original-partial-batch':
            verify_receipt(record)
            partial.append(record)
    expected = [(['number of rows updated'], 2), (['number of rows updated'], 0),
                (['number of rows deleted'], 0), (['number of rows updated'], 1),
                (['number of rows deleted'], 1), (['number of rows updated'], 0)]
    if counts != expected or len(partial) != 1 or partial[0]['receipt']['RemoteError'].get('OperationOutcome') != 'unknown':
        raise RuntimeError('Exact keyless counts or original unknown batch outcome differ')
    observed = []
    for line in (directory / 'independent-http-receipts.jsonl').read_text().splitlines():
        record = json.loads(line)
        raw = base64.b64decode(record['native_bytes'], validate=True)
        if sha(raw) != record['native_sha256']:
            raise RuntimeError('Independent original HTTP receipt changed')
        if (record['test'] == 'TestDatabendTransportLocalOriginalKeylessGuardsAndPartialBatch' and
                re.fullmatch(r'SELECT id,txt FROM "nyadb_transport_[0-9a-f]{16}"\.keyless_guards ORDER BY id,txt', record['sql'])):
            value = json.loads(raw)
            if value.get('error') is not None:
                raise RuntimeError('Independent original final read failed')
            observed += value.get('data') or []
    if observed != [['7', 'duplicate'], ['7', 'duplicate'], ['9', 'late duplicate'],
                    ['9', 'late duplicate'], ['11', 'first']]:
        raise RuntimeError('Original durable prefix, duplicate guard or unsent suffix changed')
    return {'original_statement_counts': [c[1] for c in counts],
            'partial_batch_outcome': 'unknown', 'independent_final_rows': len(observed),
            'concurrent_snapshot_commit_guarantees_accepted': False}


def verify_processes(directory):
    records = [json.loads(line) for line in (directory / 'processes.jsonl').read_text().splitlines()]
    if not records:
        raise RuntimeError('Original process identity is missing')
    for record in records:
        stat = Path('/proc') / str(record['pid']) / 'stat'
        if stat.exists():
            fields = stat.read_text().rsplit(')', 1)[1].split()
            if fields[19] == record['start_ticks']:
                raise RuntimeError('Original captured process is still alive')
    return len(records)


def run_diagnostics(command, env, logfile, timeout=540):
    with logfile.open('w') as log:
        process = subprocess.Popen(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, start_new_session=True)
        try:
            return process.wait(timeout=timeout)
        except BaseException:
            if process.poll() is None:
                os.killpg(process.pid, signal.SIGTERM)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait()
            raise


def cleanup_owned_container(base, container, nonce, secret):
    observed = json.loads(run(['docker', 'inspect', container]))[0]
    if observed['Id'] != container or observed['Config']['Labels'].get('nyadb.databend.transport') != nonce:
        raise RuntimeError('Captured fixture cleanup identity changed')
    volumes = {m['Name'] for m in observed['Mounts'] if m['Type'] == 'volume'}
    server_log = run(['docker', 'logs', container]).replace(secret.encode(), b'[redacted]')
    (base / 'server.log').write_bytes(server_log)
    run(['docker', 'rm', '-f', '-v', container])
    if volumes.intersection(run(['docker', 'volume', 'ls', '-q']).decode().splitlines()):
        raise RuntimeError('Captured anonymous volumes survived cleanup')
    return len(volumes)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--original-package', type=Path, required=True)
    parser.add_argument('--dbx', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    package, dbx = args.original_package.resolve(), args.dbx.resolve()
    pins = verify_original_package(package, dbx)
    original = source_snapshot(dbx)
    nonce = secrets.token_hex(8)
    base = ROOT / 'build/databend' / ('transport-' + nonce)
    base.mkdir(parents=True, mode=0o700)
    print('Evidence: ' + str(base), flush=True)
    (base / 'original-source.json').write_text(json.dumps(original, indent=2))
    result = {'scope': 'local_original_JDBC_transport_diagnostic', 'status': 'running',
              'image': IMAGE, 'package': str(package), 'pins': pins, 'race': args.race,
              'managed_publication_accepted': False, 'public_provider_registered': False,
              'tls_scope': 'owned validating HTTPS gateway to real authenticated Databend HTTP'}
    container = None
    secret = secrets.token_hex(24)
    username = 'nyadb_transport'
    credential = base / 'private-password'
    envfile = base / 'private-container-env'
    for path, value in [(credential, secret), (envfile, 'QUERY_DEFAULT_USER=' + username + '\nQUERY_DEFAULT_PASSWORD=' + secret + '\n')]:
        with path.open('x') as output:
            os.chmod(path, 0o600)
            output.write(value)
    try:
        container = run(['docker', 'run', '-d', '--name', 'nyadb-databend-transport-' + nonce,
                         '--label', 'nyadb.databend.transport=' + nonce, '--memory=4g', '--cpus=4',
                         '--env-file', str(envfile), '-p', '127.0.0.1::8000', IMAGE]).decode().strip()
        envfile.unlink()
        record = json.loads(run(['docker', 'inspect', container]))[0]
        binding = record['NetworkSettings']['Ports']['8000/tcp']
        if len(binding) != 1 or binding[0]['HostIp'] != '127.0.0.1':
            raise RuntimeError('Owned native server escaped local fixture')
        (base / 'original-container.json').write_text(json.dumps({
            'id': record['Id'], 'image_id': record['Image'], 'image': IMAGE,
            'labels': record['Config']['Labels'], 'ports': binding,
            'volumes': [m['Name'] for m in record['Mounts'] if m['Type'] == 'volume'],
        }, indent=2))
        endpoint = 'http://127.0.0.1:' + binding[0]['HostPort']
        journal = []

        def native_sql(sql):
            headers = {'Content-Type': 'application/json', 'Authorization': 'Basic ' + base64.b64encode((username + ':' + secret).encode()).decode()}
            request = urllib.request.Request(endpoint + '/v1/query',
                                             data=json.dumps({'sql': sql, 'pagination': {'wait_time_secs': 1, 'max_rows_per_page': 1000}}).encode(),
                                             headers=headers)
            rows = []
            for _ in range(200):
                with urllib.request.urlopen(request, timeout=20) as reply:
                    raw = reply.read()
                    value = json.loads(raw)
                    journal.append({'native_bytes': base64.b64encode(raw).decode(), 'native_sha256': sha(raw)})
                if value.get('error'):
                    raise RuntimeError('Owned native SQL failed')
                rows += value.get('data') or []
                next_uri = value.get('next_uri')
                if not next_uri:
                    return rows
                if not next_uri.startswith('/v1/query/'):
                    raise RuntimeError('Native query escaped original identity')
                request = urllib.request.Request(endpoint + next_uri, headers={'Authorization': headers['Authorization']})
            raise RuntimeError('Native query did not finish')

        deadline = time.monotonic() + 150
        while True:
            try:
                version = native_sql('SELECT version()')[0][0]
                break
            except (urllib.error.URLError, TimeoutError, ConnectionError, IndexError):
                if time.monotonic() >= deadline:
                    raise RuntimeError('Owned Databend did not become ready')
                time.sleep(1)
        result['reported_server_version'] = version
        print('Owned server: ' + version, flush=True)
        database = 'nyadb_transport_' + nonce
        native_sql('CREATE DATABASE "' + database + '"')
        native_sql('CREATE TABLE "' + database + '".original_values (id UInt64 NOT NULL, exact Decimal(38,20) NOT NULL, payload Binary NOT NULL, absent Nullable(String), txt String NOT NULL, items Array(UInt64) NOT NULL, doc Variant NOT NULL)')
        native_sql('INSERT INTO "' + database + '".original_values VALUES (9007199254740993, 9007199254740993.12345678901234567890, unhex(\'00ff80\'), NULL, \'original text\', [9007199254740993], parse_json(\'{"id":9007199254740993}\'))')
        native_sql('CREATE TABLE "' + database + '".pages (id UInt64, payload Binary)')
        native_sql('INSERT INTO "' + database + '".pages SELECT number, unhex(\'00ff\') FROM numbers(205)')
        for name in ['lost_local', 'lost_upstream']:
            native_sql('CREATE TABLE "' + database + '".' + name + ' (id UInt64)')
        (base / 'independent-http-seed.json').write_text(json.dumps(journal, indent=2))
        fixture = {'Endpoint': endpoint, 'Username': username, 'PasswordFile': str(credential),
                   'Package': str(package), 'Directory': str(base), 'Database': database}
        fixture_file = base / 'fixture.json'
        fixture_file.write_text(json.dumps(fixture, indent=2))
        env = os.environ.copy()
        env.update(NYADB_DATABEND_TRANSPORT_LOCAL_IT='1', NYADB_DATABEND_TRANSPORT_FIXTURE=str(fixture_file))
        command = ['go', 'test', '-count=1', '-v', '-timeout=8m']
        if args.race:
            command.append('-race')
        command += ['-run', '^TestDatabendTransportLocal', './internal/services/databend']
        test_code = run_diagnostics(command, env, base / 'go-test.log')
        output = (base / 'go-test.log').read_text()
        if test_code:
            raise RuntimeError('Original transport diagnostics failed; inspect ' + str(base / 'go-test.log'))
        verify_named_acceptance(output)
        result['original_executor_receipts'] = verify_receipts(base)
        result['active_cancellation'] = verify_active_cancellation(base)
        result['mutation_evidence'] = verify_mutation_evidence(base)
        result['original_processes_exited'] = verify_processes(base)
        if source_snapshot(dbx) != original or verify_original_package(package, dbx) != pins:
            raise RuntimeError('Original source/artifact snapshot changed')
        result.update(status='passed', original_source_unchanged=True, original_package_unchanged=True,
                      required_diagnostics=REQUIRED, unverified=[
                          'managed archive licensing/shaded dependency publication acceptance',
                          'public config persistence/URI/chooser/editor/provider/workspaces',
                          'native product TLS/cloud/warehouse/routes/other versions/modes',
                          'initial POST cancellation/native kill acknowledgement/shared data editing',
                          'keyless concurrency guarantees/managed mutation plans',
                      ])
    except BaseException as error:
        result.update(status='failed', error=str(error))
        raise
    finally:
        credential.unlink(missing_ok=True)
        envfile.unlink(missing_ok=True)
        if container:
            count = cleanup_owned_container(base, container, nonce, secret)
            result.update(original_container_removed=True, anonymous_volumes_removed=count)
        (base / 'verification.json').write_text(json.dumps(result, indent=2))
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
