#!/usr/bin/env python3
"""Run a disposable, owned official StarRocks FE/BE and verify native tests.

NYADB_STARROCKS_QEMU may name an extracted, verified static qemu-x86_64 on
hosts without AVX2. It emulates only the unmodified BE; no binfmt registration.
"""
import argparse
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'starrocks/allin1-ubuntu@sha256:7f6041f6d537162a2627668565630e3204756f5e85b60944de3acc2d3abd96f8'


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('invalid immutable StarRocks fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    obj = json.loads(result.stdout)[0]
    labels = obj.get('Config', {}).get('Labels') or {}
    if obj.get('Id') != identifier or labels.get('nyadb.fixture') != 'starrocks' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(plain=False, hold=False, race=False):
    base, output = ROOT / 'temp/starrocks', ROOT / 'build/starrocks'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True,
                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    nonce, identifier = uuid.uuid4().hex, None
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        secret, key_password = 'Nya' + secrets.token_hex(20) + '9', secrets.token_hex(24)
        password_file = folder / 'password'
        password_file.write_text(secret)
        password_file.chmod(0o600)
        extra, qemu_path = [], os.environ.get('NYADB_STARROCKS_QEMU')
        if qemu_path:
            qemu = Path(qemu_path).resolve(strict=True)
            if not qemu.is_file():
                raise RuntimeError('static QEMU binary required')
            # The virtual CPU information is scoped to this owned container.
            # QEMU executes the corresponding AVX2/BMI instructions; adding
            # unsupported flags to a native BE alone would not be a repair.
            text = Path('/proc/cpuinfo').read_text()
            def flags(match):
                values = set(match[1].split()) | {'avx2', 'bmi1', 'bmi2', 'f16c', 'fma'}
                return 'flags\t: ' + ' '.join(sorted(values))
            (folder / 'cpuinfo').write_text(re.sub(r'^flags\s*:\s*(.*)$', flags, text, flags=re.M))
            extra = ['--mount', 'type=bind,source=' + str(qemu) + ',target=/nyadb-qemu,readonly',
                     '--mount', 'type=bind,source=' + str(folder/'cpuinfo') + ',target=/proc/cpuinfo,readonly',
                     '-e', 'NYADB_BE_QEMU=1']
        tls = not plain
        if tls:
            ssl = folder / 'ssl'
            ssl.mkdir(mode=0o700)
            (ssl/'key-password').write_text(key_password)
            def openssl(*args):
                subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
                    '-subj', '/CN=NyaDB disposable StarRocks CA', '-keyout', str(ssl/'ca-key.pem'),
                    '-out', str(ssl/'ca-cert.pem'), '-addext', 'basicConstraints=critical,CA:TRUE',
                    '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost',
                    '-keyout', str(ssl/'server-key.pem'), '-out', str(ssl/'server.csr'))
            (ssl/'extensions').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
            openssl('x509', '-req', '-in', str(ssl/'server.csr'), '-CA', str(ssl/'ca-cert.pem'),
                    '-CAkey', str(ssl/'ca-key.pem'), '-CAcreateserial', '-days', '2', '-sha256',
                    '-extfile', str(ssl/'extensions'), '-out', str(ssl/'server-cert.pem'))
            openssl('pkcs12', '-export', '-inkey', str(ssl/'server-key.pem'), '-in', str(ssl/'server-cert.pem'),
                    '-certfile', str(ssl/'ca-cert.pem'), '-name', 'starrocks-server',
                    '-passout', 'file:'+str(ssl/'key-password'), '-out', str(ssl/'keystore.p12'))
            for file in ssl.iterdir():
                file.chmod(0o600)
        # All mutations are inside the new owned container. Preserve the
        # official BE bytes under a new filename if explicitly emulating it.
        (folder/'bootstrap.py').write_text('''import os,re
from pathlib import Path
root=Path('/data/deploy/starrocks')
p=root/'fe/conf/fe.conf'
s=p.read_text()
s=re.sub(r'-Xms[0-9]+[mMgG]', '-Xms512m', s)
s=re.sub(r'-Xmx[0-9]+[mMgG]', '-Xmx2048m', s)
if Path('/nyadb-source/ssl/key-password').exists():
 secret=Path('/nyadb-source/ssl/key-password').read_text()
 s+='\\nssl_keystore_location=/nyadb-source/ssl/keystore.p12\\nssl_keystore_password='+secret+'\\nssl_key_password='+secret+'\\n'
p.write_text(s)
if os.environ.get('NYADB_BE_QEMU')=='1':
 p=root/'be/lib/starrocks_be'
 p.rename(root/'be/lib/starrocks_be.original')
 p.write_text('#!/bin/sh\\nexec /nyadb-qemu -cpu Haswell /data/deploy/starrocks/be/lib/starrocks_be.original "$@"\\n')
 p.chmod(0o755)
os.execv('/data/deploy/entrypoint.sh',['/data/deploy/entrypoint.sh'])
''')
        database = 'nyadb_starrocks_' + nonce[:12]
        def redact(text):
            return text.replace(secret, '[redacted]').replace(key_password, '[redacted]')
        try:
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-starrocks-it-'+nonce[:12],
                '--label', 'nyadb.fixture=starrocks', '--label', 'nyadb.fixture.nonce='+nonce,
                '--memory=12g', '--cpus=4', '--shm-size=1g', '--ulimit', 'nofile=65536:65536',
                '-p', '127.0.0.1::9030', '-p', '127.0.0.1::8030', '-p', '127.0.0.1::8040',
                '--mount', 'type=bind,source='+str(folder)+',target=/nyadb-source,readonly',
                *extra, '--entrypoint', '/usr/bin/tini-static', IMAGE, '--',
                'python3', '/nyadb-source/bootstrap.py'], text=True).strip()
            assert re.fullmatch('[0-9a-f]{64}', identifier)
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            command = ['docker', 'exec', '-i', identifier, 'mysql', '--ssl-mode=DISABLED', '--batch',
                       '--raw', '--skip-column-names', '--connect-timeout=5', '-h127.0.0.1', '-P9030', '-uroot']
            deadline = time.monotonic() + (500 if qemu_path else 180)
            while True:
                obj = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
                if obj['State'].get('Health', {}).get('Status') == 'healthy':
                    break
                if not obj['State']['Running'] or time.monotonic() >= deadline:
                    raise RuntimeError('StarRocks FE/BE readiness failed; see captured owned logs')
                time.sleep(1)
            identity = subprocess.run(command, input='SET CATALOG default_catalog; SELECT VERSION(),current_version(),@@version_comment; SHOW CATALOGS; SHOW FRONTENDS; SHOW BACKENDS;\n', capture_output=True, text=True, timeout=30)
            if identity.returncode or '4.1.7-' not in identity.stdout:
                raise RuntimeError('SQL does not prove selected native StarRocks kernel')
            (output/('identity-'+nonce+'.txt')).write_text(identity.stdout)
            bootstrap = "CREATE USER 'nyadb_it' IDENTIFIED BY '"+secret+"'; GRANT 'root' TO USER 'nyadb_it'; SET DEFAULT ROLE 'root' TO 'nyadb_it'; CREATE DATABASE `"+database+"`;\n"
            result = subprocess.run(command, input=bootstrap, capture_output=True, text=True, timeout=30)
            if result.returncode:
                (output/('bootstrap-error-'+nonce+'.log')).write_text(redact(result.stdout+result.stderr))
                raise RuntimeError('owned authentication/database bootstrap failed')
            ready_sql = "CREATE TABLE IF NOT EXISTS `"+database+"`.`__nyadb_ready`(id BIGINT) DUPLICATE KEY(id) DISTRIBUTED BY HASH(id) BUCKETS 1 PROPERTIES ('replication_num'='1'); SELECT COUNT(*) FROM `"+database+"`.`__nyadb_ready`;\n"
            deadline = time.monotonic() + 180
            while True:
                ready = subprocess.run(command, input=ready_sql, capture_output=True, text=True, timeout=30)
                if ready.returncode == 0 and ready.stdout.strip() == '0':
                    break
                if time.monotonic() >= deadline:
                    (output/('readiness-error-'+nonce+'.log')).write_text(redact(ready.stdout+ready.stderr))
                    raise RuntimeError('native StarRocks storage did not become ready')
                time.sleep(1)
            port = subprocess.check_output(['docker', 'port', identifier, '9030/tcp'], text=True).strip().rsplit(':', 1)[1]
            private = folder/'probe.json'
            private.write_text(json.dumps({'endpoint':'127.0.0.1:'+port, 'username':'nyadb_it', 'password_file':str(password_file),
                'database':database, 'catalog':'default_catalog', 'ca_file':str(ssl/'ca-cert.pem') if tls else '',
                'server_name':'localhost' if tls else ''}))
            private.chmod(0o600)
            (output/('owned-fixture-'+nonce+'.json')).write_text(json.dumps({'id':identifier, 'nonce':nonce, 'image':IMAGE,
                'folder':str(folder), 'probe':str(private), 'port':port, 'database':database, 'tls':tls,
                'be_execution':'unmodified official binary under QEMU Haswell' if qemu_path else 'native host CPU'}, indent=2)+'\n')
            print('StarRocks FE/BE verified; private probe:', private, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            else:
                env = os.environ.copy()
                env.update({'NYADB_STARROCKS_IT':'1', 'NYADB_STARROCKS_CONFIG':str(private),
                    'NYADB_STARROCKS_VISUAL_DIR':str(output/('visual-'+nonce))})
                args = ['go', 'test', '-count=1', '-v', '-run', 'StarRocksNative', *(['-race'] if race else []),
                        './internal/services/starrocks', './internal/services/dbruntime', './internal/ui/shell']
                log_file = output/('acceptance-'+nonce+'.log')
                with log_file.open('w') as log:
                    result = subprocess.run(args, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=500)
                if result.returncode or 'TestStarRocksNative' not in log_file.read_text():
                    raise RuntimeError('StarRocks native acceptance failed; see captured test log')
                print('StarRocks native acceptance passed', flush=True)
        finally:
            if identifier:
                for tag, args in [('server', ['docker','logs',identifier]),
                                  ('be', ['docker','exec',identifier,'tail','-n','300','/data/deploy/starrocks/be/log/be.out']),
                                  ('fe', ['docker','exec',identifier,'tail','-n','300','/data/deploy/starrocks/fe/log/fe.log'])]:
                    result = subprocess.run(args, capture_output=True, text=True)
                    (output/(tag+'-'+nonce+'.log')).write_text(redact(result.stdout+result.stderr))
                remove_owned(identifier, nonce)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--hold', action='store_true')
    parser.add_argument('--race', action='store_true')
    args = parser.parse_args()
    verify(plain=args.plain, hold=args.hold, race=args.race)
