#!/usr/bin/env python3
"""Verify native Elasticsearch operations on an owned official Docker fixture."""
import argparse
import base64
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
IMAGE = 'docker.elastic.co/elasticsearch/elasticsearch@sha256:5dde04a980faf9105d6380c39469c645579684f1eab571f224989c22253c13e3'


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('invalid immutable Elasticsearch fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    obj = json.loads(result.stdout)[0]
    labels = obj.get('Config', {}).get('Labels') or {}
    if obj['Id'] != identifier or labels.get('nyadb.fixture') != 'elasticsearch' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Elasticsearch ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(plain=False, hold=False, race=False, profile=False):
    base, output = ROOT/'temp/elasticsearch', ROOT/'build/elasticsearch'
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
        tls = folder/'tls'
        tls.mkdir(mode=0o700)
        password = '' if plain else 'Nya'+secrets.token_hex(20)+'9'
        password_file = folder/'password'
        password_file.write_text(password)
        password_file.chmod(0o400)
        fixture_uid = 1000 if os.getuid() == 0 else os.getuid()
        if os.getuid() == 0:
            os.chown(password_file, fixture_uid, 0)
        arguments = ['--user', str(fixture_uid)+':0', '--memory=4g', '--cpus=4', '-e', 'ES_JAVA_OPTS=-Xms1g -Xmx1g', '-e', 'discovery.type=single-node', '-e', 'xpack.security.enabled='+('false' if plain else 'true')]
        if not plain:
            def openssl(*args):
                subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB disposable Elasticsearch CA', '-keyout', str(folder/'ca-key.pem'), '-out', str(tls/'ca.pem'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
            openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/CN=localhost', '-keyout', str(tls/'server-key.pem'), '-out', str(folder/'server.csr'))
            (folder/'extensions').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
            openssl('x509', '-req', '-in', str(folder/'server.csr'), '-CA', str(tls/'ca.pem'), '-CAkey', str(folder/'ca-key.pem'), '-CAcreateserial', '-CAserial', str(folder/'ca.srl'), '-days', '2', '-sha256', '-extfile', str(folder/'extensions'), '-out', str(tls/'server-cert.pem'))
            for p in tls.iterdir():
                p.chmod(0o400)
                if os.getuid() == 0:
                    os.chown(p, fixture_uid, 0)
            if os.getuid() == 0:
                os.chown(tls, fixture_uid, 0)
            arguments += ['--mount', 'type=bind,source='+str(tls)+',target=/usr/share/elasticsearch/config/nyadb-tls,readonly', '--mount', 'type=bind,source='+str(password_file)+',target=/run/nyadb/password,readonly', '-e', 'ELASTIC_PASSWORD_FILE=/run/nyadb/password', '-e', 'xpack.security.http.ssl.enabled=true', '-e', 'xpack.security.http.ssl.key=nyadb-tls/server-key.pem', '-e', 'xpack.security.http.ssl.certificate=nyadb-tls/server-cert.pem', '-e', 'xpack.security.http.ssl.certificate_authorities=nyadb-tls/ca.pem']
            arguments += ['-e', 'xpack.security.transport.ssl.enabled=true', '-e', 'xpack.security.transport.ssl.key=nyadb-tls/server-key.pem', '-e', 'xpack.security.transport.ssl.certificate=nyadb-tls/server-cert.pem', '-e', 'xpack.security.transport.ssl.certificate_authorities=nyadb-tls/ca.pem', '-e', 'xpack.security.transport.ssl.verification_mode=certificate']
        try:
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-elasticsearch-it-'+nonce[:12], '--label', 'nyadb.fixture=elasticsearch', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::9200', *arguments, IMAGE], text=True).strip()
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            port = subprocess.check_output(['docker', 'port', identifier, '9200/tcp'], text=True).strip().rsplit(':', 1)[1]
            endpoint = ('http' if plain else 'https')+'://127.0.0.1:'+port
            context = None if plain else ssl.create_default_context(cafile=str(tls/'ca.pem'))
            deadline = time.monotonic()+240
            while True:
                try:
                    request = urllib.request.Request(endpoint+'/')
                    if password:
                        request.add_header('Authorization', 'Basic '+base64.b64encode(('elastic:'+password).encode()).decode())
                    with urllib.request.urlopen(request, context=context, timeout=5) as response:
                        identity = json.load(response)
                        product = response.headers.get('X-Elastic-Product')
                    if identity['version']['number'] == '9.2.0' and identity['cluster_uuid'] != '_na_' and product == 'Elasticsearch':
                        break
                except Exception:
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError('native Elasticsearch readiness failed; inspect owned redacted log')
                time.sleep(1)
            if password:
                # ELASTIC_PASSWORD_FILE initializes a bootstrap password. Set
                # its permanent value once and await the actual security-index
                # receipt before native API-key tests. Never retry this write.
                initialize = urllib.request.Request(endpoint+'/_security/user/elastic/_password', data=json.dumps({'password': password}).encode(), method='POST', headers={'Content-Type': 'application/json'})
                initialize.add_header('Authorization', 'Basic '+base64.b64encode(('elastic:'+password).encode()).decode())
                with urllib.request.urlopen(initialize, context=context, timeout=90) as response:
                    json.load(response)
            (output/('identity-'+nonce+'.json')).write_text(json.dumps(identity, indent=2)+'\n')
            probe = folder/'probe.json'
            probe.write_text(json.dumps({'endpoint': endpoint, 'username': '' if plain else 'elastic', 'auth': 'none' if plain else 'basic', 'password_file': str(password_file), 'ca_file': '' if plain else str(tls/'ca.pem')}))
            probe.chmod(0o600)
            (output/('owned-fixture-'+nonce+'.json')).write_text(json.dumps({'id': identifier, 'nonce': nonce, 'image': IMAGE, 'folder': str(folder), 'probe': str(probe), 'tls': not plain}, indent=2)+'\n')
            print('Native Elasticsearch 9.2.0 ready; private probe:', probe, flush=True)
            if hold:
                while True:
                    time.sleep(1)
            else:
                env = os.environ.copy()
                env.update({'NYADB_ELASTICSEARCH_IT': '1', 'NYADB_ELASTICSEARCH_CONFIG': str(probe)})
                packages = ['./internal/services/elasticsearch']
                if profile:
                    packages.append('./internal/ui/connectioneditor')
                    env['NYADB_ELASTICSEARCH_PROFILE_IMAGES'] = str(output/('profile-images-'+nonce))
                args = ['go', 'test', '-count=1', '-v', '-run', 'ElasticsearchNative', *(['-race'] if race else []), *packages]
                logfile = output/(('profile-acceptance-' if profile else 'acceptance-')+nonce+'.log')
                with logfile.open('w') as log:
                    result = subprocess.run(args, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600)
                if result.returncode or 'TestElasticsearchNative' not in logfile.read_text():
                    raise RuntimeError('Elasticsearch native acceptance failed; inspect captured test log')
                if profile and '--- PASS: TestElasticsearchNativeProfileURIReopenAndEditorConnectionChecks' not in logfile.read_text():
                    raise RuntimeError('Elasticsearch profile acceptance did not execute')
                print('Native Elasticsearch acceptance passed', flush=True)
        finally:
            if identifier:
                result = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                text = result.stdout+result.stderr
                if password:
                    text = text.replace(password, '[redacted]')
                (output/('server-'+nonce+'.log')).write_text(text)
                remove_owned(identifier, nonce)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--hold', action='store_true')
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--profile', action='store_true', help='also verify saved profiles, vault lifecycle and localized editor forms')
    args = parser.parse_args()
    verify(args.plain, args.hold, args.race, args.profile)
