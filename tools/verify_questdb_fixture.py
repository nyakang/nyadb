#!/usr/bin/env python3
"""Verify QuestDB with a pinned, privately owned official Docker fixture."""
from pathlib import Path
import argparse, hashlib, json, os, secrets, shutil, subprocess, sys, time
root = Path(__file__).resolve().parents[1]
image = 'questdb/questdb@sha256:1e109276b61087010d8a3a0b033c313fcb26f6af898cbdc30f53e51cf494d0c5'
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--race', action='store_true', help='enable Go race detection')
parser.add_argument('--backend-only', action='store_true', help='run native service tests without the public UI')
args = parser.parse_args()
label = 'backend' if args.backend_only else 'public'
nonce = secrets.token_hex(8)
folder = root / 'build/questdb' / (label + '-' + nonce)
folder.mkdir(parents=True)
private = root / 'temp' / ('questdb-fixture-' + nonce)
private.mkdir(mode=448)
password = secrets.token_urlsafe(24)
secret = private / 'password'
secret.write_text(password)
secret.chmod(384)
envfile = private / 'server.env'
envfile.write_text('QDB_PG_USER=questdb\nQDB_PG_PASSWORD=' + password + '\nQDB_SHARED_WORKER_COUNT=1\nQDB_PG_WORKER_COUNT=1\nQDB_HTTP_WORKER_COUNT=1\nQDB_WAL_APPLY_WORKER_COUNT=1\n')
envfile.chmod(384)
result = dict(status='running', runner_pid=os.getpid(), image=image, checks=[], source_sha256={}, goal_complete=False)
paths = subprocess.check_output(['git', 'ls-files', '--cached', '--others', '--exclude-standard'], cwd=root, text=True).splitlines()
result['source_sha256'] = {p: hashlib.sha256((root / p).read_bytes()).hexdigest() for p in paths if p.endswith(('.go', '.py')) or Path(p).name in ('go.mod', 'go.sum', 'AGENTS.md')}

def save():
    (folder / 'result.json').write_text(json.dumps(result, indent=2) + '\n')

def verify_source():
    for p, h in result['source_sha256'].items():
        assert hashlib.sha256((root / p).read_bytes()).hexdigest() == h, p
container = None
save()
print('QuestDB gate evidence=' + str(folder) + ' runner=' + str(os.getpid()), flush=True)
try:
    inspect = json.loads(subprocess.check_output(['docker', 'image', 'inspect', image], text=True))[0]
    (folder / 'official-image.json').write_text(json.dumps(dict(Id=inspect['Id'], RepoDigests=inspect['RepoDigests'], Architecture=inspect['Architecture']), indent=2) + '\n')
    container = subprocess.check_output(['docker', 'run', '-d', '--name', 'nyadb-questdb-fixture-' + nonce, '--label', 'nyadb.owner=' + nonce, '--memory', '1g', '--cpus', '1', '--env-file', str(envfile), '-p', '127.0.0.1::8812', '-p', '127.0.0.1::9000', image], text=True).strip()
    result['original_container'] = container
    save()
    state = json.loads(subprocess.check_output(['docker', 'inspect', container], text=True))[0]
    port = state['NetworkSettings']['Ports']['8812/tcp'][0]['HostPort']
    passfile = private / 'pgpass'
    passfile.write_text('127.0.0.1:' + port + ':*:questdb:' + password + '\n')
    passfile.chmod(384)
    pg_env = dict(os.environ, PGPASSFILE=str(passfile), PGCONNECT_TIMEOUT='2', PGSSLMODE='disable')
    deadline = time.monotonic() + 45
    while True:
        ready = subprocess.run(['psql', '-X', '-h', '127.0.0.1', '-p', port, '-U', 'questdb', '-d', 'qdb', '--csv', '-v', 'ON_ERROR_STOP=1', '-c', 'SELECT build()'], env=pg_env, capture_output=True, text=True, timeout=4)
        if ready.returncode == 0:
            break
        if time.monotonic() > deadline:
            raise RuntimeError('original owned fixture readiness failed')
        time.sleep(0.2)
    assert '8.2.3' in ready.stdout
    (folder / 'version.json').write_text(json.dumps(dict(exit_code=ready.returncode, stdout=ready.stdout, stderr=ready.stderr), indent=2) + '\n')
    config = private / 'config.json'
    config.write_text(json.dumps(dict(Host='127.0.0.1', Port=int(port), Username='questdb', PasswordFile=str(secret), Image=image, Version='8.2.3', Container=container)))
    config.chmod(384)
    env = {k: v for k, v in os.environ.items() if not (k.startswith('NYADB_') and k.endswith('_IT'))}
    env.update(NYADB_QUESTDB_IT='1', NYADB_QUESTDB_CONFIG=str(config), NYADB_QUESTDB_EVIDENCE=str(folder / 'backend-evidence'), NYADB_QUESTDB_SHELL_EVIDENCE=str(folder / 'shell-evidence'), CGO_ENABLED='1')
    base = ['go', 'test'] + (['-race'] if args.race else []) + ['-count=1', '-timeout=10m', '-json']
    commands = [base + ['./internal/services/questdb']]
    if not args.backend_only:
        commands.append(base + ['./internal/ui/shell', '-run', '^TestQuestDBNative'])
    for index, command in enumerate(commands):
        start = time.monotonic()
        with (folder / ('gate-' + str(index) + '.log')).open('w') as log:
            p = subprocess.Popen(command, cwd=root, env=env, stdout=log, stderr=subprocess.STDOUT)
            check = dict(command=command, original_pid=p.pid, status='running')
            result['checks'].append(check)
            save()
            print('Original native gate pid=' + str(p.pid), flush=True)
            code = p.wait()
        passed = set()
        for line in (folder / ('gate-' + str(index) + '.log')).read_text().splitlines():
            try:
                event = json.loads(line)
            except ValueError:
                continue
            if event.get('Action') == 'pass' and event.get('Test'):
                passed.add(event['Test'])
        required = {'TestNativeQuestDBMetadataPagingAndExactTypedValues', 'TestNativeQuestDBOriginalSQLLeasesAndStreaming', 'TestNativeQuestDBPlanAndEmptyResultReceipts', 'TestNativeQuestDBCancellationRetiresOnlyOriginalLease', 'TestNativeQuestDBReadOnlyCredentialAndPoolIsolation', 'TestNativeQuestDBLostOriginalWriteReceiptCannotReplay', 'TestNativeQuestDBQueuedCancellationDoesNotDispatchOrLoseOriginalSession'} if index == 0 else {'TestQuestDBNativePublicChooserVaultSQLGridStructureAndRecovery/native', 'TestQuestDBNativePublicChooserVaultSQLGridStructureAndRecovery/read-only'}
        missing = sorted(required - passed)
        check.update(status='passed' if code == 0 and (not missing) else 'failed', exit_code=code, seconds=round(time.monotonic() - start, 2), passed_tests=sorted(passed), missing_tests=missing)
        save()
        verify_source()
        if missing:
            code = 1
        if code:
            result['status'] = 'failed'
            break
    else:
        result['status'] = 'passed'
finally:
    if container:
        state = json.loads(subprocess.check_output(['docker', 'inspect', container], text=True))[0]
        assert state['Id'] == container and state['Config']['Labels']['nyadb.owner'] == nonce
        logs = subprocess.run(['docker', 'logs', container], capture_output=True, text=True)
        (folder / 'server.log').write_text((logs.stdout + logs.stderr).replace(password, '[redacted]'))
        subprocess.run(['docker', 'rm', '-f', '-v', container], check=True, capture_output=True)
        result['original_container_removed'] = subprocess.run(['docker', 'inspect', container], capture_output=True).returncode != 0
    shutil.rmtree(private)
    result['private_credentials_removed'] = not private.exists()
    if result['status'] == 'running':
        result['status'] = 'failed'
    save()
print(json.dumps({k: v for k, v in result.items() if k != 'source_sha256'}), flush=True)
sys.exit(0 if result['status'] == 'passed' else 1)
