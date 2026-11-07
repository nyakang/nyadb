#!/usr/bin/env python3
"""Verify Easysearch on an owned Docker fixture with proper server TLS.

--compat-base runs the unchanged official distribution and bundled JDK on
Ubuntu when the official Docker base's x86-64-v3 requirement exceeds the host.
It does not change the database binary, disable certificate checks or emulate
the protocol. Compatibility deployment is recorded separately in evidence.
"""
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
IMAGE = 'infinilabs/easysearch@sha256:c2f4df07114180899cccabc4a9bab996732689bf6c44764cff320d692163fedd'
BUILD_HASH = 'c8206f7d5ec3da50ae75b7db483cefb90a0b726b'


def remove_owned(identifier, nonce):
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{64}', identifier):
        raise RuntimeError('Invalid immutable fixture ID')
    result = subprocess.run(['docker', 'inspect', identifier], capture_output=True, text=True)
    if result.returncode:
        return
    record = json.loads(result.stdout)[0]
    labels = record['Config'].get('Labels') or {}
    if record['Id'] != identifier or labels.get('nyadb.fixture') != 'easysearch' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('Fixture ownership changed; refuse cleanup')
    subprocess.run(['docker', 'rm', '-f', identifier], check=True, stdout=subprocess.DEVNULL)


def verify(compat_base=False, plain=False, race=False, profiles=False, sessions=False, documents=False):
    base, output = ROOT/'temp/easysearch', ROOT/'build/easysearch'
    base.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    nonce, identifier = uuid.uuid4().hex, None
    image = IMAGE
    def shutdown(signum, _frame):
        raise SystemExit(128+signum)
    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    with tempfile.TemporaryDirectory(prefix='fixture-', dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        password = 'Nya'+secrets.token_hex(20)+'@9'
        password_file = folder/'password'
        password_file.write_text('' if plain else password)
        password_file.chmod(0o600)
        environment = folder/'environment'
        environment.write_text('EASYSEARCH_INITIAL_ADMIN_PASSWORD='+password+'\n')
        environment.chmod(0o600)
        build_log = output/('compat-build-'+nonce+'.log')
        if compat_base:
            tag = 'nyadb-easysearch-fixture:'+nonce
            with build_log.open('w') as log:
                subprocess.run(['docker', 'build', '--label', 'nyadb.fixture=easysearch-compat', '--label', 'nyadb.fixture.nonce='+nonce, '-t', tag, '-f', str(ROOT/'tools/easysearch_fixture/Dockerfile'), str(ROOT/'tools/easysearch_fixture')], check=True, stdout=log, stderr=subprocess.STDOUT, timeout=600)
            image = subprocess.check_output(['docker', 'image', 'inspect', tag, '--format', '{{.Id}}'], text=True).strip()
        else:
            subprocess.run(['docker', 'image', 'inspect', IMAGE], check=True, stdout=subprocess.DEVNULL)
        tls = folder/'tls'
        tls.mkdir(mode=0o700)
        def openssl(*args):
            subprocess.run(['openssl', *args], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        openssl('req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2', '-subj', '/CN=NyaDB owned Easysearch CA', '-keyout', str(folder/'ca.key'), '-out', str(tls/'ca.crt'), '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign')
        openssl('req', '-new', '-newkey', 'rsa:2048', '-nodes', '-subj', '/C=IN/ST=FI/L=NI/O=ORG/OU=UNIT/CN=infini.cloud', '-keyout', str(tls/'instance.key'), '-out', str(folder/'instance.csr'))
        extensions = folder/'extensions'
        extensions.write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth,clientAuth\nsubjectAltName=DNS:infini.cloud,DNS:localhost,IP:127.0.0.1\nauthorityKeyIdentifier=keyid,issuer\n')
        openssl('x509', '-req', '-in', str(folder/'instance.csr'), '-CA', str(tls/'ca.crt'), '-CAkey', str(folder/'ca.key'), '-CAcreateserial', '-CAserial', str(folder/'ca.srl'), '-days', '2', '-sha256', '-extfile', str(extensions), '-out', str(tls/'instance.crt'))
        arguments = ['--env-file', str(environment), '--memory=3g', '--cpus=4', '-e', 'ES_JAVA_OPTS=-Xms512m -Xmx512m']
        for leaf in tls.iterdir():
            leaf.chmod(0o444 if leaf.suffix == '.crt' else 0o400)
            if os.getuid() == 0:
                os.chown(leaf, 602, 602)
            elif leaf.suffix == '.key':
                leaf.chmod(0o444)  # Private parent remains mode 0700.
            arguments += ['--mount', 'type=bind,source='+str(leaf)+',target=/app/easysearch/config/'+leaf.name+',readonly']
        if plain:
            arguments += ['-e', 'security.enabled=false', '-e', 'security.ssl.http.enabled=false']
        evidence = {'nonce': nonce, 'source_image': IMAGE, 'runtime_image': image, 'deployment': 'unchanged_official_distribution_and_JDK_on_Ubuntu' if compat_base else 'official_Docker_base', 'tls': not plain, 'build_hash': BUILD_HASH}
        evidence_path = output/('owned-fixture-'+nonce+'.json')
        try:
            identifier = subprocess.check_output(['docker', 'create', '--name', 'nyadb-easysearch-it-'+nonce[:12], '--label', 'nyadb.fixture=easysearch', '--label', 'nyadb.fixture.nonce='+nonce, '-p', '127.0.0.1::9200', *arguments, image], text=True).strip()
            evidence['id'] = identifier
            evidence_path.write_text(json.dumps(evidence, indent=2)+'\n')
            subprocess.run(['docker', 'start', identifier], check=True, stdout=subprocess.DEVNULL)
            port = subprocess.check_output(['docker', 'port', identifier, '9200/tcp'], text=True).strip().rsplit(':', 1)[1]
            endpoint = ('http' if plain else 'https')+'://127.0.0.1:'+port
            context = None if plain else ssl.create_default_context(cafile=str(tls/'ca.crt'))
            deadline = time.monotonic()+240
            while True:
                state = subprocess.check_output(['docker', 'inspect', identifier, '--format', '{{.State.Status}}'], text=True).strip()
                if state in ['exited', 'dead']:
                    raise RuntimeError('Owned Easysearch terminated; inspect sanitized server log')
                try:
                    request = urllib.request.Request(endpoint+'/')
                    if not plain:
                        request.add_header('Authorization', 'Basic '+base64.b64encode(('admin:'+password).encode()).decode())
                    with urllib.request.urlopen(request, context=context, timeout=5) as response:
                        identity = json.load(response)
                    version = identity.get('version', {})
                    if version.get('distribution') == 'easysearch' and version.get('number') == '2.5.1' and version.get('build_hash') == BUILD_HASH and identity.get('cluster_uuid') not in [None, '', '_na_']:
                        break
                except Exception:
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError('Native Easysearch readiness failed')
                time.sleep(1)
            (output/('identity-'+nonce+'.json')).write_text(json.dumps(identity, indent=2)+'\n')
            probe = folder/'probe.json'
            probe.write_text(json.dumps({'endpoint': endpoint, 'auth': 'none' if plain else 'basic', 'username': '' if plain else 'admin', 'password_file': str(password_file), 'ca_file': '' if plain else str(tls/'ca.crt'), 'server_name': ''}))
            probe.chmod(0o600)
            print('Native Easysearch 2.5.1 ready; fixture nonce: '+nonce, flush=True)
            env = os.environ.copy()
            env.update({'NYADB_EASYSEARCH_IT': '1', 'NYADB_EASYSEARCH_CONFIG': str(probe)})
            sources = sorted((ROOT/'internal/services/easysearch').glob('*.go'))+[Path(__file__).resolve()]
            source_hashes = {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest() for path in sources}
            evidence['source_sha256'] = source_hashes
            logfile = output/('acceptance-'+nonce+'.log')
            packages = ['./internal/services/connections', './internal/storage/localdb', './internal/ui/connectioneditor'] if profiles else ['./internal/services/easysearch', './internal/services/connections']
            if profiles:
                env['NYADB_EASYSEARCH_PROFILE_IMAGES'] = str(output/('profile-images-'+nonce))
            args = ['go', 'test', '-count=1', '-v', *(['-race'] if race else []), *packages]
            with logfile.open('w') as log:
                result = subprocess.run(args, cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=600)
            text = logfile.read_text()
            required = ['TransportAuthenticationDocumentsAndPIT', 'LostWriteReceiptRetiresOriginalWithoutReplay', 'CancellationSettlesOriginalHTTPWorkerAndReleasesPIT', 'PITKeepsEqualIDsOnDifferentRoutingShards', 'ScrollReadsCompleteOriginalSnapshotBeyondPITWindow', 'ScrollLostResponseDoesNotReplayAdvancedResource', 'IncompletePITReceiptRetiresAndCleansCapturedResource']
            if profiles:
                required = ['ProfileURIReopenAndEditorConnectionChecks', 'EditorLocalizedFormEvidence']
                evidence['scope'] = 'profile_uri_vault_injected_restart_editor'
                evidence['images'] = str(Path(env['NYADB_EASYSEARCH_PROFILE_IMAGES']).relative_to(ROOT))
            else:
                if not plain:
                    required.append('AccessTokenUsesNativeHeaderAndPermissions')
                if sessions or documents:
                    required += ['SessionIndexAliasDataStreamMetadataAndREST', 'MetadataRejectsCapturedIndexReplacementAndAliasChange', 'QueryLostCommittedReplyRequiresExplicitRecoveryWithoutReplay', 'AliasCaseKeepsQualifiedIdentity']
                    evidence['scope'] = 'native_metadata_and_request_sessions'
                if documents:
                    required += ['DataDocumentExactRowsPagingFieldsFilteredExportAndCleanup', 'DataDocumentAliasScopeAndStreamConcreteIdentity', 'DataDocumentRejectsSameNameIndexRecreation', 'DataDocumentBoundedGridAndCompleteFrozenScrollExport', 'DataDocumentKeepsEqualIDsOnRoutingShards', 'DataExportLostAdvanceFailsWithoutReplayAndReleasesScroll', 'DataCloseCancelsUnpublishedPageAndKeepsOtherDocument', 'DataRejectsScopeChangeAfterReadReceipt']
                    evidence['scope'] = 'private_shared_data_document_reads_and_complete_scroll_exports'
            if any(hashlib.sha256((ROOT/path).read_bytes()).hexdigest() != digest for path, digest in source_hashes.items()):
                raise RuntimeError('Verification sources changed during acceptance; rerun on stable sources')
            if result.returncode or any('--- PASS: TestEasysearchNative'+name not in text for name in required):
                raise RuntimeError('Native Easysearch acceptance failed; inspect captured test log')
            evidence.update({'status': 'passed_with_race' if race else 'passed', 'log': str(logfile.relative_to(ROOT)), 'log_sha256': hashlib.sha256(logfile.read_bytes()).hexdigest()})
            print('Native Easysearch acceptance passed', flush=True)
        finally:
            if identifier:
                logs = subprocess.run(['docker', 'logs', identifier], capture_output=True, text=True)
                text = (logs.stdout+logs.stderr).replace(password, '[redacted]')
                lines = [('[redacted sensitive startup line]' if re.search(r'password|token|authorization|secret', line, re.I) else line) for line in text.splitlines()]
                (output/('server-'+nonce+'.log')).write_text('\n'.join(lines)+'\n')
                remove_owned(identifier, nonce)
                evidence['fixture_cleaned'] = True
            evidence_path.write_text(json.dumps(evidence, indent=2)+'\n')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--compat-base', action='store_true')
    parser.add_argument('--plain', action='store_true')
    parser.add_argument('--race', action='store_true')
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--documents', action='store_true', help='verify bound shared grid, field reads and complete native Scroll exports')
    mode.add_argument('--sessions', action='store_true', help='verify metadata and native request-session contracts')
    mode.add_argument('--profiles', action='store_true', help='verify native profile, restart and localized connection form')
    args = parser.parse_args()
    verify(args.compat_base, args.plain, args.race, args.profiles, args.sessions, args.documents)
