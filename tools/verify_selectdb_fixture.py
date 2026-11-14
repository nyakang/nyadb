#!/usr/bin/env python3
"""Verify official SelectDB FE/BE, native runtime and UI using owned fixtures."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import shutil
import subprocess
import tempfile
import time

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--race', action='store_true')
parser.add_argument('--plain', action='store_true', help='verify a plaintext fixture instead of TLS')
parser.add_argument('--qemu', default=os.environ.get('NYADB_SELECTDB_QEMU'), help='verified static QEMU executable when the host lacks AVX2')
args = parser.parse_args()
ROOT = Path(__file__).resolve().parents[1]
FE = 'selectdb/selectdb.fe-ubuntu@sha256:a7e38759762ba93d6a7bf9f35d69dd1e88fcb3f63da005eecaf499c17850d0fb'
BE = 'selectdb/selectdb.be-ubuntu@sha256:ff47782b69aa571a259145c6e94bd2751e026431934b687c743e337763ee7fe8'
OUT = ROOT / 'build/mysql-products/selectdb'
OUT.mkdir(parents=True, exist_ok=True)
NONCE = secrets.token_hex(8)
OWNED = []

def run(args, **kw):
    return subprocess.run(args, check=True, text=True, **kw)

def create(image, extra, command):
    identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-selectdb-' + NONCE + '-' + ('fe' if image == FE else 'be'),
        '--label', 'nyadb.fixture=selectdb', '--label', 'nyadb.fixture.nonce=' + NONCE,
        '--memory=10g', '--cpus=4', '--ulimit', 'nofile=65536:65536', *extra, '--entrypoint', '/usr/bin/python3', image, '-c', command], text=True).strip()
    assert re.fullmatch('[a-f0-9]{64}', identifier)
    OWNED.append(identifier)
    return identifier

def stop(signum, frame):
    raise SystemExit(128 + signum)

signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
folder = ROOT / 'temp/selectdb-fixture' / NONCE
folder.mkdir(mode=0o700)
secret = 'Nya' + secrets.token_hex(20) + '9'
(folder / 'password').write_text(secret)
(folder / 'password').chmod(0o600)
qemu = Path(args.qemu).resolve(strict=True) if args.qemu else None
emulation = ' avx2 ' not in ' ' + Path('/proc/cpuinfo').read_text().split('flags', 1)[1].split('\n', 1)[0] + ' '
tls = not args.plain
if tls:
    ssl = folder / 'ssl'
    ssl.mkdir(mode=0o700)
    def openssl(*arguments):
        run(['openssl', *arguments], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB disposable SelectDB CA',
        '-keyout', str(ssl/'ca-key.pem'), '-out', str(ssl/'ca-cert.pem'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
    openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', str(ssl/'server-key.pem'), '-out', str(ssl/'server.csr'))
    (ssl/'extensions').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
    openssl('x509', '-req', '-in', str(ssl/'server.csr'), '-CA', str(ssl/'ca-cert.pem'), '-CAkey', str(ssl/'ca-key.pem'), '-CAcreateserial', '-days', '2', '-sha256', '-extfile', str(ssl/'extensions'), '-out', str(ssl/'server-cert.pem'))
    openssl('pkcs12', '-export', '-inkey', str(ssl/'server-key.pem'), '-in', str(ssl/'server-cert.pem'), '-certfile', str(ssl/'ca-cert.pem'), '-name', 'selectdb-server', '-passout', 'pass:doris', '-out', str(ssl/'server_certificate.p12'))
    openssl('pkcs12', '-export', '-nokeys', '-in', str(ssl/'ca-cert.pem'), '-name', 'nyadb-ca', '-jdktrust', 'anyExtendedKeyUsage', '-passout', 'pass:doris', '-out', str(ssl/'ca_certificate.p12'))
    for file in ssl.iterdir():
        file.chmod(0o600)
fe_command = r'''from pathlib import Path
import os,re
p=Path('/opt/apache-doris/fe/conf/fe.conf')
s=p.read_text()
s=re.sub(r'-Xmx8192m','-Xmx2048m',s)
s=re.sub(r'-Xms8192m','-Xms512m',s)
s+='\nenable_fqdn_mode=false\n'
if Path('/nyadb-source/ssl/server_certificate.p12').exists():
 s+='enable_ssl=true\nmysql_ssl_default_ca_certificate=/nyadb-source/ssl/ca_certificate.p12\nmysql_ssl_default_server_certificate=/nyadb-source/ssl/server_certificate.p12\n'
p.write_text(s)
os.execv('/opt/apache-doris/fe/bin/start_fe.sh',['start_fe.sh','--console'])
'''
be_command = r'''from pathlib import Path
import os,re
p=Path('/opt/apache-doris/be/conf/be.conf')
s=p.read_text()
s+='\nmem_limit=3G\nwebserver_num_workers=4\nbrpc_num_threads=4\n'
s+='pipeline_executor_size=4\n'
p.write_text(s)
start=Path('/opt/apache-doris/be/bin/start_be.sh')
s=start.read_text()
# The host has 1,048,576 maps and this tiny fixture needs far fewer. Retain
# the actual host limit; bypass only this launch-script prerequisite rather
# than writing a host sysctl or changing any native engine binary.
block=''' + repr('''    if [[ "${MAX_MAP_COUNT}" -lt 2000000 ]]; then
        echo "Please set vm.max_map_count to be 2000000 under root using 'sysctl -w vm.max_map_count=2000000'."
        exit 1
    fi''') + r'''
assert s.count(block)==1
s=s.replace(block,'    echo "NyaDB tiny fixture uses the actual existing host map limit: ${MAX_MAP_COUNT}"')
start.write_text(s)
if Path('/nyadb-qemu').exists():
 p=Path('/opt/apache-doris/be/lib/doris_be')
 p.rename(p.with_name('doris_be.original'))
 p.write_text('#!/bin/sh\nexec /nyadb-qemu -cpu Haswell /opt/apache-doris/be/lib/doris_be.original "$@"\n')
 p.chmod(0o755)
os.execv(str(start),[str(start),'--console'])
'''
compile(fe_command, '<FE bootstrap>', 'exec')
compile(be_command, '<BE bootstrap>', 'exec')
try:
    fe = create(FE, ['-p', '127.0.0.1::9030', '--mount', 'type=bind,source='+str(folder)+',target=/nyadb-source,readonly'], fe_command)
    run(['docker', 'start', fe], stdout=subprocess.DEVNULL)
    print('Started owned SelectDB FE', fe, flush=True)
    mysql = ['docker', 'exec', '-i', fe, 'mysql', '--batch', '--raw', '--skip-column-names', '--connect-timeout=5', '-h127.0.0.1', '-P9030', '-uroot']
    deadline = time.monotonic() + 150
    while True:
        identity = subprocess.run(mysql, input='SELECT VERSION(),@@version_comment,CURRENT_CATALOG(),@@sql_mode;\n', capture_output=True, text=True, timeout=10)
        if identity.returncode == 0:
            break
        state = json.loads(subprocess.check_output(['docker', 'inspect', fe], text=True))[0]
        if not state['State']['Running'] or time.monotonic() >= deadline:
            raise RuntimeError('FE readiness failed')
        time.sleep(1)
    (OUT / 'identity.txt').write_text(identity.stdout)
    print('Identity:', identity.stdout.strip(), flush=True)
    extra = ['--network', 'container:' + fe]
    if emulation:
        if qemu is None:
            raise RuntimeError('host lacks AVX2; supply a verified static --qemu for the owned BE only')
        expected = '307ec5c1c8461b380955ba437261e9e99e0994e5d8643a2a6e0321a1ce2bc4bb'
        assert hashlib.sha256(qemu.read_bytes()).hexdigest() == expected
        extra += ['--mount', 'type=bind,source=' + str(qemu) + ',target=/nyadb-qemu,readonly']
    be = create(BE, extra, be_command)
    run(['docker', 'start', be], stdout=subprocess.DEVNULL)
    print('Started owned SelectDB BE', be, 'emulated=' + str(emulation), flush=True)
    # Both nodes share the owned FE network namespace; the registration is
    # reachable only there and the single exposed query port is loopback.
    run(mysql, input='ALTER SYSTEM ADD BACKEND "127.0.0.1:9050";\n', stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    port = subprocess.check_output(['docker', 'port', fe, '9030/tcp'], text=True).strip().rsplit(':', 1)[1]
    database = 'nyadb_selectdb_' + NONCE
    bootstrap = "CREATE DATABASE `" + database + "`; CREATE USER 'nyadb_it'@'%' IDENTIFIED BY '" + secret + "'; GRANT ADMIN_PRIV ON *.*.* TO 'nyadb_it'@'%';\n"
    run(mysql, input=bootstrap, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
    config = folder / 'probe.json'
    config.write_text(json.dumps({'endpoint': '127.0.0.1:' + port, 'username': 'nyadb_it', 'password_file': str(folder / 'password'), 'database': database, 'catalog': 'internal', 'ca_file': str(ssl/'ca-cert.pem') if tls else '', 'server_name': 'localhost' if tls else ''}))
    config.chmod(0o600)
    state = {'owner': NONCE, 'fe': fe, 'be': be, 'fe_image': FE, 'be_image': BE, 'config': str(config), 'be_execution': 'unmodified official BE under verified QEMU Haswell' if emulation else 'native host CPU', 'launch_map_check': 'tiny fixture retains actual host limit; launch-script check bypassed', 'status': 'initializing', 'tls': tls, 'race': args.race, 'version': identity.stdout.strip(), 'qemu_sha256': expected if emulation else None}
    (OUT / 'current-fixture.json').write_text(json.dumps(state, indent=2) + '\n')
    ready_sql = 'CREATE TABLE IF NOT EXISTS `' + database + '`.`nyadb_ready` (id BIGINT) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ("replication_num"="1"); SELECT COUNT(*) FROM `' + database + '`.`nyadb_ready`;\n'
    deadline = time.monotonic() + (600 if emulation else 150)
    while True:
        result = subprocess.run(mysql, input=ready_sql, capture_output=True, text=True, timeout=20)
        if result.returncode == 0 and result.stdout.strip() == '0':
            break
        be_state = json.loads(subprocess.check_output(['docker', 'inspect', be], text=True))[0]
        if not be_state['State']['Running'] or time.monotonic() >= deadline:
            (OUT / 'readiness-error.log').write_text((result.stdout + result.stderr).replace(secret, '[redacted]'))
            raise RuntimeError('BE storage readiness failed')
        time.sleep(1)
    state['status'] = 'ready'
    (OUT / 'current-fixture.json').write_text(json.dumps(state, indent=2) + '\n')
    print('Owned real SelectDB fixture ready:', config, flush=True)
    env = os.environ.copy()
    env.update(NYADB_SELECTDB_IT='1', NYADB_SELECTDB_CONFIG=str(config), NYADB_SELECTDB_VISUAL_DIR=str(OUT/'ui'))
    command = ['go', 'test', '-count=1', '-v', '-run', '^TestSelectDB', *(['-race'] if args.race else []),
        './internal/services/doris', './internal/services/selectdb', './internal/services/dbruntime',
        './internal/services/connections', './internal/storage/localdb', './internal/db/queryplan',
        './internal/ui/connectioneditor', './internal/ui/connectioncatalog', './internal/ui/shell']
    log_path = OUT/'acceptance.log'
    with log_path.open('w') as log:
        result = subprocess.run(command, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600)
    text = log_path.read_text()
    required = ['TestSelectDBNativeCatalogPrecisionExportAndOriginalLeaseFixture',
        'TestSelectDBNativeAuthenticationAndProductIdentityFixture', 'TestSelectDBNativeAggregateStateBytesFixture',
        'TestSelectDBNativeRuntimePublicationFixture', 'TestSelectDBNativeShellFixture',
        'TestSelectDBChooserOpensIndependentNativeEditor', 'TestSelectDBNativeEditorTLSCredentialsAndRollback',
        'TestSelectDBIdentityCatalogAndTemporaryURISecret', 'TestSelectDBProfileRestartAndForeignEnvelopeRejection',
        'TestSelectDBNativeNamedScanAndCost']
    if result.returncode or any('--- PASS: '+name+' ' not in text for name in required):
        raise RuntimeError('SelectDB required real-product or public workflow acceptance failed; inspect the captured log')
    state.update(status='passed', runtime_and_native_shell='passed')
    (OUT/'fixture-verification.json').write_text(json.dumps(state, indent=2)+'\n')
    print('SelectDB real runtime, original leases, native UI and publication acceptance passed', flush=True)
finally:
    for identifier in reversed(OWNED):
        obj = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
        assert obj['Id'] == identifier and obj['Config']['Labels']['nyadb.fixture.nonce'] == NONCE and obj['Config']['Labels']['nyadb.fixture'] == 'selectdb'
        logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
        (OUT / (identifier[:12] + '-server.log')).write_text((logs.stdout + logs.stderr).replace(secret, '[redacted]'))
        subprocess.run(['docker', 'cp', identifier + ':/opt/apache-doris/' + ('fe' if identifier == OWNED[0] else 'be') + '/log', str(OUT / (identifier[:12] + '-logs'))], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        captured = OUT / (identifier[:12] + '-logs')
        if captured.exists():
            for path in captured.rglob('*'):
                if path.is_file():
                    raw = path.read_bytes()
                    path.write_bytes(raw.replace(secret.encode(), b'[redacted]'))
        run(['docker', 'rm', '-f', identifier], stdout=subprocess.DEVNULL)
        print('Removed owned fixture', identifier, flush=True)

    shutil.rmtree(folder)
    if (OUT/'current-fixture.json').exists():
        current = json.loads((OUT/'current-fixture.json').read_text())
        if current.get('owner') == NONCE:
            current['status'] = 'closed'
            (OUT/'current-fixture.json').write_text(json.dumps(current, indent=2)+'\n')
