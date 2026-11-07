#!/usr/bin/env python3
"""Verify native Trino against a disposable, owned official coordinator."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import signal
import ssl
import subprocess
import tempfile
import time
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[1]
IMAGE = 'trinodb/trino@sha256:2704eb53935af5e582d63d6033e9bf99d20c325e9b0e30b1016ef59a4773979f'


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('invalid immutable Trino fixture ID')
    r = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if r.returncode:
        return
    obj = json.loads(r.stdout)[0]
    labels = obj.get('Config', {}).get('Labels') or {}
    if obj['Id'] != identifier or labels.get('nyadb.fixture') != 'trino' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Trino ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(plain=False, hold=False, race=False):
    base, output = ROOT/'temp/trino', ROOT/'build/trino'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
    nonce, identifier = uuid.uuid4().hex, None
    def terminate(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        cfg = folder/'config'
        cfg.mkdir(mode=0o700)
        catalogs = folder/'catalog'
        catalogs.mkdir(mode=0o700)
        for name in ['memory', 'tpch']:
            (catalogs/(name+'.properties')).write_text('connector.name='+name+'\n')
        password = '' if plain else 'Nya'+secrets.token_hex(20)+'9'
        secret = secrets.token_hex(32)
        password_file = folder/'password'
        password_file.write_text(password)
        password_file.chmod(0o600)
        props = 'coordinator=true\nnode-scheduler.include-coordinator=true\nhttp-server.http.port=8080\ndiscovery.uri=http://localhost:8080\ninternal-communication.shared-secret='+secret+'\n'
        if not plain:
            def openssl(*args):
                subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB disposable Trino CA', '-keyout', str(cfg/'ca-key.pem'), '-out', str(cfg/'ca-cert.pem'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', str(cfg/'server-key.pem'), '-out', str(cfg/'server.csr'))
            (cfg/'extensions').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
            openssl('x509', '-req', '-in', str(cfg/'server.csr'), '-CA', str(cfg/'ca-cert.pem'), '-CAkey', str(cfg/'ca-key.pem'), '-CAcreateserial', '-days', '2', '-sha256', '-extfile', str(cfg/'extensions'), '-out', str(cfg/'server-cert.pem'))
            (cfg/'server.pem').write_bytes((cfg/'server-key.pem').read_bytes()+(cfg/'server-cert.pem').read_bytes())
            salt = secrets.token_bytes(16)
            hashed = hashlib.pbkdf2_hmac('sha1', password.encode(), salt, 100000)
            (cfg/'password.db').write_text('nyadb_it:100000:'+salt.hex()+':'+hashed.hex()+'\n')
            (cfg/'password-authenticator.properties').write_text('password-authenticator.name=file\nfile.password-file=/nyadb-config/password.db\n')
            props += 'http-server.https.enabled=true\nhttp-server.https.port=8443\nhttp-server.https.keystore.path=/nyadb-config/server.pem\nhttp-server.authentication.type=PASSWORD\n'
        (cfg/'config.properties').write_text(props)
        for p in cfg.iterdir():
            p.chmod(0o600)
        port = '8080' if plain else '8443'
        try:
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-trino-it-'+nonce[:12], '--label', 'nyadb.fixture=trino', '--label', 'nyadb.fixture.nonce='+nonce, '--memory=6g', '--cpus=4', '--user=0', '-p', '127.0.0.1::'+port, '--mount', 'type=bind,source='+str(cfg)+',target=/nyadb-config,readonly', '--mount', 'type=bind,source='+str(cfg/'config.properties')+',target=/etc/trino/config.properties,readonly', '--mount', 'type=bind,source='+str(catalogs)+',target=/etc/trino/catalog,readonly', *([] if plain else ['--mount', 'type=bind,source='+str(cfg/'password-authenticator.properties')+',target=/etc/trino/password-authenticator.properties,readonly']), IMAGE], text=True).strip()
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            published = subprocess.check_output(['docker', 'port', identifier, port+'/tcp'], text=True).strip().rsplit(':', 1)[1]
            endpoint = ('http' if plain else 'https')+'://127.0.0.1:'+published
            context = None if plain else ssl.create_default_context(cafile=str(cfg/'ca-cert.pem'))
            deadline = time.monotonic()+240
            while True:
                try:
                    request = urllib.request.Request(endpoint+'/v1/info')
                    if password:
                        request.add_header('Authorization', 'Basic '+base64.b64encode(('nyadb_it:'+password).encode()).decode())
                    with urllib.request.urlopen(request, context=context, timeout=5) as r:
                        identity = json.load(r)
                    if identity['nodeVersion']['version'] == '484' and identity['coordinator'] and not identity['starting']:
                        break
                except Exception:
                    pass
                if time.monotonic()>=deadline:
                    raise RuntimeError('native Trino readiness failed; inspect owned redacted log')
                time.sleep(1)
            (output/('identity-'+nonce+'.json')).write_text(json.dumps(identity, indent=2)+'\n')
            probe = folder/'probe.json'
            probe.write_text(json.dumps({'endpoint':endpoint, 'username':'nyadb_it', 'password_file':str(password_file), 'catalog':'memory', 'schema':'default', 'ca_file':'' if plain else str(cfg/'ca-cert.pem')}))
            probe.chmod(0o600)
            (output/('owned-fixture-'+nonce+'.json')).write_text(json.dumps({'id':identifier, 'nonce':nonce, 'image':IMAGE, 'folder':str(folder), 'probe':str(probe), 'tls':not plain}, indent=2)+'\n')
            print('Native Trino 484 ready; private probe:', probe, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            else:
                env = os.environ.copy()
                env.update({'NYADB_TRINO_IT':'1', 'NYADB_TRINO_CONFIG':str(probe)})
                args = ['go','test','-count=1','-v','-run','TrinoNative', *(['-race'] if race else []), './internal/services/trino']
                logfile = output/('acceptance-'+nonce+'.log')
                with logfile.open('w') as log:
                    result = subprocess.run(args, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=240)
                if result.returncode or 'TestTrinoNative' not in logfile.read_text():
                    raise RuntimeError('Trino native acceptance failed; inspect captured test log')
                print('Native Trino acceptance passed', flush=True)
        finally:
            if identifier:
                result = subprocess.run(['docker','logs',identifier], capture_output=True, text=True)
                text = result.stdout+result.stderr
                for value in [password,secret]:
                    if value:
                        text = text.replace(value,'[redacted]')
                (output/('server-'+nonce+'.log')).write_text(text)
                remove_owned(identifier,nonce)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain',action='store_true')
    parser.add_argument('--hold',action='store_true')
    parser.add_argument('--race',action='store_true')
    args=parser.parse_args()
    verify(args.plain,args.hold,args.race)
