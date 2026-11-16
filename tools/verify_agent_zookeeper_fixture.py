#!/usr/bin/env python3
"""Verify the original DBX v1 agent against an owned official ZooKeeper 3.9.4.

By default this accepts the managed native protocol foundation. --provider also
accepts injected provider/document/workspace workflows. --public-runtime also
verifies the real builtin provider with persisted URI settings and original
document isolation/recovery. --public additionally accepts the real
chooser/editor/shell workflow, including localized UI and lost-write recovery.
--security additionally verifies digest ACLs, TLS/mTLS, mandatory SASL Digest
(server FIPS mode disabled), secure editor checks and original receipt recovery.
--topology also verifies a real three-member ensemble and authenticated TLS/mTLS.
Foundation modes never accept the public entry. SSH routes, Kerberos
and other server versions remain separately unverified.
Requires Linux, Docker, Go and the complete original source in temp/dbx.
"""
import argparse
import base64
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import secrets
import signal
import socket
import ssl
import subprocess
import time
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DBX = ROOT / 'temp/dbx'
SOURCE = DBX / 'agents/drivers/zookeeper'
IMAGE = 'zookeeper@sha256:dfa9ba46d14bd52bcb3c1dac3ba91f179cf4cebf7ee576e1c2df09c68fc1d30d'
REQUIRED = [
    'TestZooKeeperAgentNativeManagedValuesStatListingAndNamespace',
    'TestZooKeeperAgentNativeEphemeralOwnershipAndExplicitProcessClose',
    'TestZooKeeperAgentNativeCommittedLostReplyCancellationQueueAndRecovery',
    'TestZooKeeperAgentNativeEndpointRoutesRequireCompleteCapturedMapping',
]
PROVIDER_REQUIRED = [
    'TestZooKeeperNativeProviderOriginalValuesNamespaceListingAndDocumentOwnership',
    'TestZooKeeperNativeWorkspaceConfirmationParkedReceiptFilesAndUnknownRecovery',
    'TestZooKeeperNativeWorkspaceCommittedLostReplyCancellationRecoveryAndNoReplay',
]
PUBLIC_RUNTIME_REQUIRED = [
    'TestZooKeeperNativePublicRuntimeURIStoragePinnedDriverIsolationAndRecovery',
]
PUBLIC_REQUIRED = [
    'TestZooKeeperNativePublicBootstrapEditorImportVaultTestAndLiveSession',
    'TestZooKeeperNativePublicShellChooserNavigationDocumentsFilesAndRecovery',
    'TestZooKeeperNativePublicWorkspaceLostReplyDisconnectRecoveryAndNoReplay',
]
SECURITY_REQUIRED = [
    'TestZooKeeperNativePublicDigestACLIsolationAndDeniedWriteReceipt',
    'TestZooKeeperNativePublicTLSMutualTLSCapturedTrustAndRecovery',
    'TestZooKeeperNativePublicSASLRequiredAuthenticationAndOriginalReceipt',
    'TestZooKeeperNativePublicEditorSecurityFilesVaultTestAndCancellation',
]
TOPOLOGY_REQUIRED = [
    'TestZooKeeperNativePublicEnsembleFailoverKeepsOriginalDocumentSession',
    'TestZooKeeperNativePublicAuthenticatedTLSAndMutualTLSOriginalReceipts',
]
EDITOR_CONTROLS = {
    'general': ['ZooKeeper Chroot', 'ZooKeeper Namespace', 'ZooKeeper 驱动包', '导入驱动包', '连接密码'],
    'advanced': ['ZooKeeper 其他服务器', 'ZooKeeper 会话超时（毫秒）', 'ZooKeeper 连接超时（毫秒）', 'ZooKeeper 文档连接上限'],
    'ssl': ['ZooKeeper TLS 模式', 'ZooKeeper CA 证书', '选择 ZooKeeper CA 证书', 'ZooKeeper 客户端证书', 'ZooKeeper 客户端私钥'],
}


def acceptance_requirements(provider=False, public_runtime=False, public=False, security=False, topology=False):
    security = security or topology
    required = list(REQUIRED)
    if provider or public_runtime or public or security:
        required += PROVIDER_REQUIRED
    if public_runtime or public or security:
        required += PUBLIC_RUNTIME_REQUIRED
    if public or security:
        required += PUBLIC_REQUIRED
    if security:
        required += SECURITY_REQUIRED
    if topology:
        required += TOPOLOGY_REQUIRED
    return required


def prepare_security_material(base):
    directory = base / 'security'
    directory.mkdir()
    for name in ['ca', 'wrong-ca']:
        run(['openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '2',
             '-keyout', str(directory / (name + '.key')), '-out', str(directory / (name + '.pem')),
             '-addext', 'basicConstraints=critical,CA:TRUE', '-addext', 'keyUsage=critical,keyCertSign,cRLSign',
             '-subj', '/CN=owned-zookeeper-fixture-' + name])
    for name, issuer, usage in [('server', 'ca', 'serverAuth'), ('client', 'ca', 'clientAuth'), ('wrong-client', 'wrong-ca', 'clientAuth')]:
        run(['openssl', 'req', '-new', '-newkey', 'rsa:2048', '-nodes', '-keyout', str(directory / (name + '.key')),
             '-out', str(directory / (name + '.csr')), '-subj', '/CN=owned-zookeeper-fixture-' + name])
        extension = directory / (name + '.ext')
        extension.write_text('basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=' + usage + '\n' + ('subjectAltName=IP:127.0.0.1\n' if name == 'server' else ''))
        run(['openssl', 'x509', '-req', '-days', '2', '-in', str(directory / (name + '.csr')),
             '-CA', str(directory / (issuer + '.pem')), '-CAkey', str(directory / (issuer + '.key')), '-CAcreateserial',
             '-out', str(directory / (name + '.pem')), '-extfile', str(extension)])
    password = 'owned-fixture-keystore-password'
    run(['openssl', 'pkcs12', '-export', '-name', 'server', '-inkey', str(directory / 'server.key'),
         '-in', str(directory / 'server.pem'), '-certfile', str(directory / 'ca.pem'),
         '-out', str(directory / 'server.p12'), '-passout', 'pass:' + password])
    run(['keytool', '-importcert', '-noprompt', '-alias', 'fixture-ca', '-file', str(directory / 'ca.pem'),
         '-keystore', str(directory / 'trust.p12'), '-storetype', 'PKCS12', '-storepass', password])
    # Only disposable fixture material is mounted. Java runs under the image's
    # own user, which needs read access to the generated files.
    for path in directory.iterdir():
        path.chmod(0o644)
    return directory, password


def prepare_security_servers(base, token, containers, topology=False):
    directory, password = prepare_security_material(base)
    username, digest_secret, sasl_secret = 'fixtureuser', 'owned-digest-secret', 'owned-sasl-secret'
    jaas = directory / 'jaas.conf'
    jaas.write_text('Server { org.apache.zookeeper.server.auth.DigestLoginModule required user_fixtureuser="' + sasl_secret + '"; };\nClient { org.apache.zookeeper.server.auth.DigestLoginModule required username="' + username + '" password="' + sasl_secret + '"; };\n')
    endpoints = {}
    modes = ['tls', 'mtls', 'sasl'] + (['sasl-tls', 'sasl-mtls'] if topology else [])
    for mode in modes:
        secure = mode != 'sasl'
        mutual = mode in ['mtls', 'sasl-mtls']
        sasl = mode.startswith('sasl')
        config = directory / (mode + '.cfg')
        text = 'tickTime=500\ndataDir=/data\nclientPort=2181\nadmin.enableServer=false\n4lw.commands.whitelist=srvr,ruok\n'
        if secure:
            text += 'secureClientPort=2281\nserverCnxnFactory=org.apache.zookeeper.server.NettyServerCnxnFactory\nssl.enabledProtocols=TLSv1.3,TLSv1.2\nssl.keyStore.location=/security/server.p12\nssl.keyStore.type=PKCS12\nssl.keyStore.password=' + password + '\nssl.trustStore.location=/security/trust.p12\nssl.trustStore.type=PKCS12\nssl.trustStore.password=' + password + '\nssl.clientAuth=' + ('need' if mutual else 'none') + '\n'
        if sasl:
            text += 'authProvider.1=org.apache.zookeeper.server.auth.SASLAuthenticationProvider\nenforce.auth.enabled=true\nenforce.auth.schemes=sasl\n'
        if mutual:
            text += 'ssl.clientHostnameVerification=false\n'
        config.write_text(text)
        internal = '2281' if secure else '2181'
        cid = run(['docker', 'create', '--name', 'nyadb-zookeeper-' + token + '-' + mode,
                   '--label', 'nyadb.zookeeper.fixture=' + token, '-p', '127.0.0.1::' + internal,
                   '-v', str(directory) + ':/security:ro',
                   '-e', 'SERVER_JVMFLAGS=' + ('-Djava.security.auth.login.config=/security/jaas.conf -Dzookeeper.fips-mode=false' if sasl else ''),
                   '-e', 'CLIENT_JVMFLAGS=' + ('-Djava.security.auth.login.config=/security/jaas.conf -Dzookeeper.fips-mode=false' if sasl else '-Dzookeeper.sasl.client=false'),
                   IMAGE, 'zkServer.sh', 'start-foreground', '/security/' + mode + '.cfg']).decode().strip()
        containers.append((mode, cid))
        run(['docker', 'start', cid])
        inspect = json.loads(run(['docker', 'inspect', cid]))[0]
        (base / (mode + '-container.json')).write_text(json.dumps(inspect, indent=2))
        port = int(inspect['NetworkSettings']['Ports'][internal + '/tcp'][0]['HostPort'])
        endpoint = '127.0.0.1:' + str(port)
        deadline = time.monotonic() + 60
        last_error = ''
        while True:
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=1) as conn:
                    if secure:
                        context = ssl.create_default_context(cafile=str(directory / 'ca.pem'))
                        if mutual:
                            context.load_cert_chain(str(directory / 'client.pem'), str(directory / 'client.key'))
                        conn = context.wrap_socket(conn, server_hostname='127.0.0.1')
                    conn.sendall(b'ruok')
                    if conn.recv(32) == b'imok':
                        break
            except OSError as error:
                last_error = str(error)
            if time.monotonic() >= deadline:
                raise RuntimeError('Owned ' + mode + ' ZooKeeper did not become ready: ' + last_error)
            time.sleep(.2)
        endpoints[mode] = endpoint
        # Real Java client authors protected fixture nodes; native agent tests
        # independently prove both permitted and denied operations.
        acl = 'sasl:' + username + ':cdrwa' if sasl else 'world:anyone:cdrwa'
        commands = 'create /nyadb_secure fixture ' + acl + '\nget /nyadb_secure\nquit\n'
        completed = subprocess.run(['docker', 'exec', '-i', cid, 'zkCli.sh', '-server', '127.0.0.1:2181'],
            input=commands.encode(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
        output = completed.stdout.decode(errors='replace')
        if completed.returncode or 'Created /nyadb_secure' not in output or 'fixture' not in output:
            raise RuntimeError('Real Java secure fixture authoring failed: ' + mode + '\n' + output[-4000:])
        (base / (mode + '-seed.log')).write_text(output)
        if topology and not sasl:
            seed_digest_acl(base, cid, {'username': username, 'digest_secret': digest_secret}, mode + '-digest-seed.log')
    return {'tls_endpoint': endpoints['tls'], 'mtls_endpoint': endpoints['mtls'], 'sasl_endpoint': endpoints['sasl'],
            'sasl_tls_endpoint': endpoints.get('sasl-tls', ''), 'sasl_mtls_endpoint': endpoints.get('sasl-mtls', ''),
            'ca_file': str(directory / 'ca.pem'), 'wrong_ca_file': str(directory / 'wrong-ca.pem'),
            'client_cert_file': str(directory / 'client.pem'), 'client_key_file': str(directory / 'client.key'),
            'wrong_client_cert_file': str(directory / 'wrong-client.pem'), 'wrong_client_key_file': str(directory / 'wrong-client.key'),
            'username': username, 'digest_secret': digest_secret, 'sasl_secret': sasl_secret}


def seed_digest_acl(base, container, security, log='digest-seed.log'):
    credential = security['username'] + ':' + security['digest_secret']
    identity = security['username'] + ':' + base64.b64encode(hashlib.sha1(credential.encode()).digest()).decode()
    commands = 'create /nyadb_digest fixture digest:' + identity + ':cdrwa\nquit\n'
    completed = subprocess.run(['docker', 'exec', '-i', container, 'zkCli.sh', '-server', '127.0.0.1:2181'],
        input=commands.encode(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
    output = completed.stdout.decode(errors='replace')
    if completed.returncode or 'Created /nyadb_digest' not in output or security['digest_secret'] in output:
        raise RuntimeError('Real Java digest ACL fixture authoring failed')
    (base / log).write_text(output)


def prepare_ensemble(base, token, containers, networks):
    name = 'nyadb-zookeeper-' + token + '-ensemble'
    network = run(['docker', 'network', 'create', '--label', 'nyadb.zookeeper.fixture=' + token, name]).decode().strip()
    networks.append(network)
    servers = ' '.join(f'server.{i}=zk{i}:2888:3888;2181' for i in [1, 2, 3])
    endpoints, members = [], []
    reserved = []
    try:
        for i in [1, 2, 3]:
            reservation = socket.socket()
            reserved.append(reservation)
            reservation.bind(('127.0.0.1', 0))
            port = reservation.getsockname()[1]
            endpoints.append('127.0.0.1:' + str(port))
            mode = 'ensemble-' + str(i)
            cid = run(['docker', 'create', '--name', 'nyadb-zookeeper-' + token + '-' + mode,
                '--network', network, '--network-alias', 'zk' + str(i), '--label', 'nyadb.zookeeper.fixture=' + token,
                '-p', f'127.0.0.1:{port}:2181', '-e', 'ZOO_MY_ID=' + str(i), '-e', 'ZOO_SERVERS=' + servers,
                '-e', 'ZOO_TICK_TIME=500', '-e', 'ZOO_INIT_LIMIT=20', '-e', 'ZOO_SYNC_LIMIT=10',
                '-e', 'ZOO_4LW_COMMANDS_WHITELIST=srvr,ruok', IMAGE]).decode().strip()
            containers.append((mode, cid))
            members.append(cid)
        for i, cid in enumerate(members):
            # Docker receives an explicit original port, so stop/start cannot
            # silently publish a replacement endpoint. Hold it until startup.
            reserved[i].close()
            run(['docker', 'start', cid])
            inspected = json.loads(run(['docker', 'inspect', cid]))[0]
            (base / f'ensemble-{i+1}-container.json').write_text(json.dumps(inspected, indent=2))
    finally:
        for reservation in reserved:
            reservation.close()
    roles = []
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        roles = ensemble_roles(endpoints)
        if roles.count('leader') == 1 and roles.count('follower') == 2:
            break
        time.sleep(.2)
    else:
        raise RuntimeError('Owned three-member ensemble did not elect one leader and two followers')
    commands = 'create /nyadb_ensemble fixture\ncreate /nyadb_ensemble/ns namespace\nget /nyadb_ensemble/ns\nquit\n'
    seeded = subprocess.run(['docker', 'exec', '-i', members[0], 'zkCli.sh', '-server', '127.0.0.1:2181'],
        input=commands.encode(), stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=60)
    output = seeded.stdout.decode(errors='replace')
    if seeded.returncode or 'Created /nyadb_ensemble/ns' not in output:
        raise RuntimeError('Real Java ensemble fixture authoring failed: ' + output[-4000:])
    (base / 'ensemble-seed.log').write_text(output)
    (base / 'ensemble-roles.json').write_text(json.dumps({'roles': roles, 'containers': members, 'endpoints': endpoints}, indent=2))
    return {'endpoints': endpoints, 'containers': members, 'token': token}


def ensemble_roles(endpoints):
    roles = []
    for endpoint in endpoints:
        try:
            host, port = endpoint.rsplit(':', 1)
            with socket.create_connection((host, int(port)), timeout=1) as conn:
                conn.sendall(b'srvr')
                reply = b''
                while True:
                    chunk = conn.recv(8192)
                    if not chunk:
                        break
                    reply += chunk
            match = re.search(rb'^Mode: (leader|follower)\r?$', reply, re.M)
            roles.append(match.group(1).decode() if match else '')
        except OSError:
            roles.append('')
    return roles


def verify_ensemble_observations(topology, inspected, roles):
    if len(inspected) != 3 or len(roles) != 3 or roles.count('leader') != 1 or roles.count('follower') != 2:
        raise RuntimeError('Restarted ensemble did not restore three-member quorum')
    for i, member in enumerate(inspected):
        if member.get('Id') != topology['containers'][i] or member.get('Config', {}).get('Labels', {}).get('nyadb.zookeeper.fixture') != topology['token']:
            raise RuntimeError('Restarted original ensemble member ownership changed')
        if member.get('State', {}).get('Running') is not True:
            raise RuntimeError('Restarted original ensemble member is not running')
        mapped = member.get('NetworkSettings', {}).get('Ports', {}).get('2181/tcp') or []
        if len(mapped) != 1 or mapped[0].get('HostIp') != '127.0.0.1' or '127.0.0.1:' + str(mapped[0].get('HostPort')) != topology['endpoints'][i]:
            raise RuntimeError('Restarted original ensemble endpoint changed')


def verify_ensemble_restart(base, topology):
    inspected = [json.loads(run(['docker', 'inspect', member]))[0] for member in topology['containers']]
    roles = ensemble_roles(topology['endpoints'])
    verify_ensemble_observations(topology, inspected, roles)
    result = {'original_endpoints_retained': True, 'roles': roles, 'endpoints': topology['endpoints'], 'containers': topology['containers']}
    (base / 'ensemble-restored.json').write_text(json.dumps({'observations': inspected, **result}, indent=2))
    return result


def cleanup_owned_container(base, mode, container, token):
    inspected = json.loads(run(['docker', 'inspect', container]))[0]
    if inspected.get('Id') != container or inspected.get('Config', {}).get('Labels', {}).get('nyadb.zookeeper.fixture') != token:
        raise RuntimeError('Fixture container ownership changed')
    volumes = {mount['Name'] for mount in inspected.get('Mounts', []) if mount.get('Type') == 'volume'}
    (base / (mode + '-cleanup-container.json')).write_text(json.dumps(inspected, indent=2))
    (base / (mode + '.log')).write_bytes(run(['docker', 'logs', container]))
    # Docker removes only this original container's anonymous volumes with -v;
    # named volumes and resources belonging to another container stay intact.
    run(['docker', 'rm', '-f', '-v', container])
    if volumes.intersection(run(['docker', 'volume', 'ls', '-q']).decode().splitlines()):
        raise RuntimeError('Owned anonymous volumes remained after cleanup')
    return len(volumes)


def verify_public_images(base):
    from PIL import Image
    controls = json.loads((base / 'public-editor-controls.json').read_text())
    images = []
    for language in ['zh-Hans', 'en']:
        for width, height in [(920, 780), (720, 520)]:
            for tab, labels in EDITOR_CONTROLS.items():
                name = f'{language}-{width}x{height}-public-editor-{tab}'
                images.append((name + '.png', (width, height)))
                for label in labels:
                    box = controls.get(name, {}).get(label, {})
                    coordinates = [box.get(key) for key in ['X', 'Y', 'W', 'H']]
                    if any(type(n) not in [int, float] or not math.isfinite(n) for n in coordinates):
                        raise RuntimeError('Localized public control evidence missing: ' + name + ':' + label)
                    x, y, w, h = coordinates
                    expected = controls.get(f'{language}-920x780-public-editor-{tab}', {}).get(label, {}).get('H')
                    if type(expected) not in [int, float] or not math.isfinite(expected) or expected < 12 or box.get('ExpectedHeight') != expected:
                        raise RuntimeError('Localized full control height missing: ' + name + ':' + label)
                    if x < 0 or y < 29 or w <= 0 or h < expected or x + w > width + 1 or y + h > height - 45:
                        raise RuntimeError('Localized public control clipped: ' + name + ':' + label)
                    screenshot = box.get('Screenshot')
                    if not isinstance(screenshot, str) or Path(screenshot).name != screenshot or not screenshot.startswith(name) or not screenshot.endswith('.png'):
                        raise RuntimeError('Localized control screenshot missing: ' + name + ':' + label)
                    images.append((screenshot, (width, height)))
            images.append((f'{language}-{width}x{height}-public-chooser.png', (width, height)))
        for width, height in [(1280, 800), (720, 520)]:
            for stage in ['confirmation', 'receipt', 'recovery']:
                images.append((f'{language}-{width}x{height}-public-shell-{stage}.png', (width, height)))
            images.append((f'{language}-{width}x{height}-public-unknown-write.png', (width, height)))
    return verify_images(base, images)


def verify_security_images(base):
    images = []
    for language in ['zh-Hans', 'en']:
        for width, height in [(920, 780), (720, 520)]:
            for mode in ['tls', 'mtls', 'sasl']:
                images.append((f'{language}-{width}x{height}-security-editor-{mode}.png', (width, height)))
        for width, height in [(1280, 800), (720, 520)]:
            for stage in ['digest-denied-write', 'tls-recovery', 'mtls-recovery', 'sasl-receipt']:
                images.append((f'{language}-{width}x{height}-security-{stage}.png', (width, height)))
    return verify_images(base, images)


def verify_topology_images(base):
    images = []
    for language in ['zh-Hans', 'en']:
        for width, height in [(1280, 800), (720, 520)]:
            for stage in ['ensemble-failover', 'digest-tls', 'digest-mtls', 'sasl-tls', 'sasl-mtls']:
                images.append((f'{language}-{width}x{height}-security-{stage}.png', (width, height)))
    return verify_images(base, images)


def verify_topology_receipts(base):
    evidence = []
    for mode in ['ensemble', 'digest-tls', 'digest-mtls', 'sasl-tls', 'sasl-mtls']:
        path = base / f'topology-{mode}-receipts.json'
        raw = path.read_bytes()
        records = json.loads(raw)
        if not isinstance(records, dict):
            raise RuntimeError('Topology receipt records missing')
        for name, method in [('write', 'kv_put'), ('verified', 'kv_get')]:
            verify_security_receipt(records.get(name), method)
        node = records['verified']['Result']['ZNode']['Reply']['Node']
        if node.get('Value') != base64.b64encode(bytes([0, 255, 128])).decode() or node.get('Stat', {}).get('version') != 0:
            raise RuntimeError('Topology independent original binary write verification differs')
        if type(records.get('generation')) is not int or records['generation'] <= 0:
            raise RuntimeError('Topology original generation missing')
        if mode == 'ensemble':
            for name, method in [('root', 'kv_get'), ('created', 'kv_put'), ('before', 'kv_get'), ('after', 'kv_get'), ('closed', 'kv_get')]:
                verify_security_receipt(records.get(name), method)
            before = records['before']['Result']['ZNode']['Reply']['Node']
            after = records['after']['Result']['ZNode']['Reply']['Node']
            owner = records.get('ephemeral_owner')
            if type(owner) is not int or owner == 0 or before.get('Stat', {}).get('ephemeralOwner') != owner or after.get('Stat', {}).get('ephemeralOwner') != owner or before.get('Value') != after.get('Value'):
                raise RuntimeError('Topology failover replaced original ephemeral owner/value')
            failed, surviving = records.get('failed_member'), records.get('surviving_member')
            if type(failed) is not int or type(surviving) is not int or failed not in [0, 1, 2] or surviving not in [0, 1, 2] or failed == surviving:
                raise RuntimeError('Topology original socket did not move to a surviving member')
            if type(records.get('original_pid')) is not int or records['original_pid'] <= 0 or records.get('original_process_starts_during_failover') != 0 or records.get('root_identity') != '/nyadb_ensemble/ns':
                raise RuntimeError('Topology failover lacks original process/root identity')
            if records['closed']['Result']['ZNode']['Reply'].get('Found') is not False:
                raise RuntimeError('Topology original document close left its ephemeral value')
        else:
            verify_security_receipt(records.get('read'), 'kv_get')
            if records.get('wrong_ca_rejected') is not True:
                raise RuntimeError('Topology combined authentication bypassed server trust')
            if mode.endswith('mtls') and records.get('missing_client_rejected') is not True:
                raise RuntimeError('Topology combined authentication lacks mandatory client identity rejection')
            if mode.startswith('digest'):
                verify_security_receipt(records.get('denied'), 'kv_get', denied=True)
            else:
                rejected = records.get('rejected') or {}
                if rejected.get('original_process_starts') != 1 or rejected.get('test_deadline_expired') is not False or not isinstance(rejected.get('error'), str) or not rejected['error']:
                    raise RuntimeError('Topology secure SASL rejection lacks original process evidence')
        evidence.append({'path': path.name, 'sha256': digest(raw)})
    return evidence


def verify_images(base, images):
    from PIL import Image
    evidence = []
    for name, dimensions in dict(images).items():
        path = base / name
        with Image.open(path) as img:
            if img.format != 'PNG' or img.size != dimensions:
                raise RuntimeError('Public screenshot dimensions differ: ' + name)
            img.load()
            colors = img.convert('RGB').getcolors(maxcolors=16)
            if colors is not None:
                raise RuntimeError('Public screenshot lacks rendered UI pixels: ' + name)
        evidence.append({'path': name, 'sha256': digest(path.read_bytes())})
    return evidence


def verify_security_receipt(receipt, method, denied=False):
    if not isinstance(receipt, dict) or not isinstance(receipt.get('NativeBytes'), str):
        raise RuntimeError('Security receipt missing original native bytes')
    try:
        raw = base64.b64decode(receipt['NativeBytes'], validate=True)
        native = json.loads(raw)
    except (ValueError, UnicodeError) as error:
        raise RuntimeError('Security receipt has malformed native bytes') from error
    if not raw or receipt.get('NativeSHA256') != digest(raw):
        raise RuntimeError('Security receipt original byte hash differs')
    if not isinstance(native, dict) or native.get('jsonrpc') != '2.0' or type(native.get('id')) is not int:
        raise RuntimeError('Security receipt original JSON-RPC envelope missing')
    for secret in [b'owned-digest-secret', b'owned-sasl-secret']:
        if secret in raw:
            raise RuntimeError('Security receipt exposed fixture credentials')
    result = receipt.get('Result', {})
    znode = result.get('ZNode') or {}
    if result.get('Request', {}).get('Method') != method or result.get('Attempted') is not True or znode.get('Captured') is not True or znode.get('Acknowledged') is not True:
        raise RuntimeError('Security receipt original acknowledgement missing')
    if denied:
        error = native.get('error')
        if not isinstance(error, dict) or 'result' in native or 'not authenticated' not in str(error.get('message')) or type(error.get('code')) is not int or error['code'] != znode.get('ErrorCode'):
            raise RuntimeError('Security receipt missing original ACL denial')
        if result.get('Completed') is not False or result.get('Code') != 'NativeError' or result.get('WriteOutcomeUnknown') is not (method == 'kv_put'):
            raise RuntimeError('Security receipt lost independent denied write outcome')
    else:
        if result.get('Completed') is not True or result.get('WriteOutcomeUnknown') is not False or result.get('Code') != 'OK' or result.get('ProjectionError') != '' or znode.get('ErrorCode') is not None or 'error' in native or 'result' not in native:
            raise RuntimeError('Security receipt missing successful original result')
        try:
            projected = json.loads(result.get('JSON', ''))
        except (ValueError, TypeError) as error:
            raise RuntimeError('Security receipt missing original result JSON') from error
        if projected != native['result']:
            raise RuntimeError('Security receipt result differs from original native envelope')
        if method == 'kv_get':
            reply = znode.get('Reply') or {}
            if type(projected.get('found')) is not bool or projected['found'] is not reply.get('Found'):
                raise RuntimeError('Security receipt found projection differs from original envelope')
            if projected['found']:
                value = projected.get('value') or {}
                try:
                    original = base64.b64decode(value['data'], validate=True) if value.get('encoding') == 'base64' else value['data'].encode() if value.get('encoding') == 'utf8' else None
                except (ValueError, KeyError, AttributeError) as error:
                    raise RuntimeError('Security receipt original value encoding malformed') from error
                node = reply.get('Node') or {}
                if original is None or node.get('Value') != base64.b64encode(original).decode():
                    raise RuntimeError('Security receipt value projection differs from original envelope')
                metadata = projected.get('metadata') or {}
                for name in ['version', 'ephemeralOwner']:
                    if type(metadata.get(name)) is not int or node.get('Stat', {}).get(name) != metadata[name]:
                        raise RuntimeError('Security receipt stat projection differs from original envelope')
    return raw


def verify_security_receipts(base):
    evidence = []
    for mode in ['digest', 'tls', 'mtls', 'sasl']:
        path = base / f'security-{mode}-receipts.json'
        raw = path.read_bytes()
        records = json.loads(raw)
        if not isinstance(records, dict):
            raise RuntimeError('Security receipt records missing: ' + mode)
        for secret in [b'owned-digest-secret', b'owned-sasl-secret']:
            if secret in raw:
                raise RuntimeError('Security evidence exposed fixture credentials')
        for name, method in [('read', 'kv_get'), ('write', 'kv_put'), ('verified', 'kv_get')]:
            verify_security_receipt(records.get(name), method)
        if mode == 'digest':
            denied = records.get('denied')
            if not isinstance(denied, list) or len(denied) != 4:
                raise RuntimeError('Security digest evidence missing each denied read/write')
            for index, receipt in enumerate(denied):
                verify_security_receipt(receipt, 'kv_put' if index % 2 else 'kv_get', denied=True)
            node = records['verified']['Result']['ZNode']['Reply']['Node']
            if node.get('Value') != base64.b64encode(b'original-authenticated-value').decode() or node.get('Stat', {}).get('version') != 1:
                raise RuntimeError('Security digest independent data/version verification differs')
        else:
            node = records['verified']['Result']['ZNode']['Reply']['Node']
            if node.get('Value') != base64.b64encode(bytes([0, 255, 128])).decode():
                raise RuntimeError('Security binary value verification differs: ' + mode)
            if type(records.get('generation')) is not int or records['generation'] <= 0:
                raise RuntimeError('Security original generation missing: ' + mode)
        if mode in ['tls', 'mtls']:
            original = verify_security_receipt(records.get('original'), 'kv_get')
            recovered = verify_security_receipt(records.get('recovered'), 'kv_get')
            if original != recovered:
                raise RuntimeError('Security recovery replaced original receipt bytes: ' + mode)
        if mode == 'sasl':
            rejected = records.get('rejected')
            if not isinstance(rejected, list) or [probe.get('mode') for probe in rejected] != ['wrong-secret', 'none', 'digest']:
                raise RuntimeError('Security SASL negative probes missing')
            for probe in rejected:
                if not isinstance(probe.get('error'), str) or not probe['error'] or probe.get('original_process_starts') != 1 or probe.get('test_deadline_expired') is not False:
                    raise RuntimeError('Security SASL rejection lacks original process evidence')
        evidence.append({'path': path.name, 'sha256': digest(raw)})
    return evidence


def digest(raw):
    return hashlib.sha256(raw).hexdigest()


def run(args, cwd=ROOT, env=None, timeout=600):
    completed = subprocess.run(args, cwd=cwd, env=env, stdout=subprocess.PIPE,
                               stderr=subprocess.STDOUT, timeout=timeout)
    if completed.returncode:
        raise RuntimeError('Command failed: ' + str(args[:3]) + '\n' + completed.stdout.decode(errors='replace')[-12000:])
    return completed.stdout


def source_snapshot():
    files = {}
    for directory in [SOURCE, DBX / 'agents/go-common/gosasl', DBX / 'agents/go-common/go-gssapi']:
        if not directory.is_dir():
            raise RuntimeError('Complete original source missing: ' + str(directory))
        for path in sorted(directory.rglob('*')):
            if path.is_file() and not any(part in ['.git', 'build'] for part in path.relative_to(directory).parts):
                files[str(path.relative_to(DBX))] = digest(path.read_bytes())
    files['LICENSE'] = digest((DBX / 'LICENSE').read_bytes())
    return files


def nyadb_snapshot():
    paths = sorted(set(run(['git', 'ls-files', '-z', '--cached', '--others', '--exclude-standard']).decode().split('\0')) - {''})
    return {name: digest((ROOT / name).read_bytes()) for name in paths}


def json_stream(raw):
    decoder = json.JSONDecoder()
    text, offset = raw.decode(), 0
    while offset < len(text):
        while offset < len(text) and text[offset].isspace():
            offset += 1
        if offset == len(text):
            return
        value, offset = decoder.raw_decode(text, offset)
        yield value


def verify_acceptance(output, required=REQUIRED):
    for name in required:
        if not re.search(r'^--- PASS: ' + re.escape(name) + r' \(', output, re.M):
            raise RuntimeError('Required native acceptance missing: ' + name)


def build_bundle(base, before):
    env = dict(os.environ, GOWORK='off', CGO_ENABLED='0')
    binary = base / 'original-agent'
    run(['go', 'build', '-mod=readonly', '-o', str(binary), '.'], SOURCE, env)
    dependencies = run(['go', 'list', '-mod=readonly', '-deps', '-json', '.'], SOURCE, env)
    (base / 'original-dependencies.jsonl').write_bytes(dependencies)
    build_info = run(['go', 'version', '-m', str(binary)])
    (base / 'original-build-info.txt').write_bytes(build_info)
    if source_snapshot() != before:
        raise RuntimeError('Building modified the original source or dependency manifests')
    payload = {'bin/agent': binary.read_bytes(), 'licenses/dbx-LICENSE': (DBX / 'LICENSE').read_bytes()}
    modules = {}
    for package in json_stream(dependencies):
        module = package.get('Module')
        if module:
            modules[module['Path']] = module
    module_evidence = []
    for index, (name, module) in enumerate(sorted(modules.items())):
        location = module.get('Replace', module)
        directory = Path(location['Dir'])
        licenses = [p for p in sorted(directory.rglob('*')) if p.is_file() and re.match(r'^(license|licence|notice|copying|copyright)([._-]|$)', p.name, re.I)]
        if name != 'github.com/t8y2/dbx/agents/drivers/zookeeper' and not licenses:
            raise RuntimeError('Compiled module license missing: ' + name)
        names = []
        for number, path in enumerate(licenses):
            target = 'licenses/module-' + str(index) + '-' + str(number) + '.txt'
            payload[target] = path.read_bytes()
            names.append({'path': str(path.relative_to(directory)), 'payload': target, 'sha256': digest(payload[target])})
        module_evidence.append({'path': name, 'version': module.get('Version'),
                                'sum': module.get('Sum'), 'replace': module.get('Replace'), 'licenses': names})
    goroot = Path(run(['go', 'env', 'GOROOT']).decode().strip())
    payload['licenses/go-LICENSE'] = (goroot / 'LICENSE').read_bytes()
    payload['sources.json'] = json.dumps({'scope': 'unmodified original ZooKeeper v1 agent',
        'source_files': before, 'compiled_modules': module_evidence,
        'binary_sha256': digest(payload['bin/agent']), 'build_info': build_info.decode()}, indent=2).encode()
    version = 'dbx-v1-' + digest(payload['bin/agent'])[:16]
    manifest = {'format': 1, 'key': 'zookeeper', 'version': version,
        'platform': 'linux-amd64', 'runtime': 'native', 'executable': 'bin/agent',
        'profiles': ['zookeeper-v1'], 'files': [
            {'path': name, 'sha256': digest(raw), **({'executable': True} if name == 'bin/agent' else {})}
            for name, raw in sorted(payload.items())],
        'licenses': sorted(name for name in payload if name.startswith('licenses/')), 'provenance': 'sources.json'}
    archive = base / 'zookeeper-driver.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as bundle:
        bundle.writestr('manifest.json', json.dumps(manifest, indent=2))
        for name, raw in sorted(payload.items()):
            bundle.writestr(name, raw)
    return archive, digest(archive.read_bytes()), version


def process_state(record):
    pid = int(record['pid'])
    try:
        stat = Path('/proc/' + str(pid) + '/stat').read_text()
        fields = stat[stat.rfind(')') + 1:].split()
        executable = os.readlink('/proc/' + str(pid) + '/exe')
        return fields[19] == record['start_ticks'] and executable == record['executable']
    except (FileNotFoundError, ProcessLookupError):
        return False


def cleanup_processes(base):
    journal = base / 'processes.jsonl'
    records = [json.loads(line) for line in journal.read_text().splitlines()] if journal.exists() else []
    cleaned = []
    for record in records:
        if process_state(record):
            pid = int(record['pid'])
            if os.getpgid(pid) != pid:
                raise RuntimeError('Original process group ownership changed')
            os.killpg(pid, signal.SIGKILL)
            deadline = time.monotonic() + 5
            while process_state(record) and time.monotonic() < deadline:
                time.sleep(.02)
            if process_state(record):
                raise RuntimeError('Original captured process still alive after termination')
            cleaned.append(record['pid'])
    return {'recorded': len(records), 'forced_cleanup': cleaned,
            'all_original_processes_exited': not any(process_state(record) for record in records)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--race', action='store_true')
    parser.add_argument('--provider', action='store_true')
    parser.add_argument('--public-runtime', action='store_true')
    parser.add_argument('--public', action='store_true')
    parser.add_argument('--security', action='store_true')
    parser.add_argument('--topology', action='store_true')
    args = parser.parse_args()
    if args.topology:
        args.security = True
    if args.security:
        args.public = True
    if args.public:
        args.public_runtime = True
    if args.public_runtime:
        args.provider = True
    if platform.system() != 'Linux' or platform.machine() not in ['x86_64', 'amd64']:
        raise RuntimeError('This pinned acceptance requires Linux amd64')
    token = secrets.token_hex(8)
    base = ROOT / 'build/zookeeper' / ('agent-' + token)
    base.mkdir(parents=True)
    print('Evidence: ' + str(base.relative_to(ROOT)), flush=True)
    nyadb_before = nyadb_snapshot()
    (base / 'nyadb-source-snapshot.json').write_text(json.dumps({'files': nyadb_before,
        'base_commit': run(['git', 'rev-parse', 'HEAD']).decode().strip()}, indent=2))
    before = source_snapshot()
    (base / 'accepted-original-source.json').write_text(json.dumps(before, indent=2))
    archive, archive_digest, version = build_bundle(base, before)
    required = acceptance_requirements(args.provider, args.public_runtime, args.public, args.security, args.topology)
    result = {'scope': 'public-native-topology-and-security-workflow' if args.topology else 'public-native-security-workflow' if args.security else 'public-native-workflow' if args.public else 'public-runtime-profile-foundation' if args.public_runtime else 'private-native-provider-workspace' if args.provider else 'managed-native-protocol-foundation', 'public_entry_accepted': False,
              'public_runtime_accepted': False,
              'security_accepted': False,
              'topology_accepted': False,
              'product': 'ZooKeeper', 'server_version': '3.9.4', 'image': IMAGE,
              'race': args.race, 'driver_sha256': archive_digest,
              'passed_tests': [], 'unverified': ([] if args.public else ['public editor/chooser/navigation/shell workflow' if args.public_runtime else 'public runtime/editor/chooser/URI' if args.provider else 'public provider/editor/chooser/URI/KV workspace']) + [
                  'TLS/mTLS', 'digest ACL enforcement', 'SASL authentication', 'ensemble failover', 'SSH routes', 'other server versions']}
    container = None
    containers = []
    networks = []
    try:
        container = run(['docker', 'run', '-d', '--name', 'nyadb-zookeeper-' + token,
            '--label', 'nyadb.zookeeper.fixture=' + token, '-p', '127.0.0.1::2181',
            '-e', 'ZOO_TICK_TIME=500', '-e', 'ZOO_4LW_COMMANDS_WHITELIST=srvr,ruok',
            IMAGE]).decode().strip()
        containers.append(('server', container))
        inspect = json.loads(run(['docker', 'inspect', container]))[0]
        (base / 'container.json').write_text(json.dumps(inspect, indent=2))
        port = int(inspect['NetworkSettings']['Ports']['2181/tcp'][0]['HostPort'])
        endpoint = '127.0.0.1:' + str(port)
        deadline = time.monotonic() + 60
        while True:
            try:
                with socket.create_connection(('127.0.0.1', port), timeout=1) as conn:
                    conn.sendall(b'ruok')
                    if conn.recv(32) == b'imok':
                        break
            except OSError:
                pass
            if time.monotonic() >= deadline:
                raise RuntimeError('Owned ZooKeeper did not become ready')
            time.sleep(.2)
        fixture = base / 'fixture.json'
        security = prepare_security_servers(base, token, containers, args.topology) if args.security else None
        if security:
            seed_digest_acl(base, container, security)
        topology = prepare_ensemble(base, token, containers, networks) if args.topology else None
        fixture.write_text(json.dumps({'archive': str(archive), 'sha256': archive_digest,
            'version': version, 'directory': str(base), 'endpoint': endpoint, 'security': security, 'topology': topology}, indent=2))
        fixture.chmod(0o600)
        env = dict(os.environ, CGO_ENABLED='1' if args.race else '0',
            NYADB_ZOOKEEPER_PROVIDER_IT='1' if args.provider else '0',
            NYADB_ZOOKEEPER_PUBLIC_RUNTIME_IT='1' if args.public_runtime else '0',
            NYADB_ZOOKEEPER_PUBLIC_IT='1' if args.public else '0',
            NYADB_ZOOKEEPER_SECURITY_IT='1' if args.security else '0',
            NYADB_ZOOKEEPER_TOPOLOGY_IT='1' if args.topology else '0',
            NYADB_AGENT_ZOOKEEPER_IT='1', NYADB_AGENT_ZOOKEEPER_FIXTURE_FILE=str(fixture))
        command = ['go', 'test', '-count=1', '-v', '-timeout=10m' if args.public else '-timeout=5m']
        if args.race:
            command += ['-race']
        command += ['./internal/services/agentbridge']
        if args.provider:
            command += ['./internal/ui/kvworkspace']
        if args.public:
            command += ['./internal/bootstrap', './internal/ui/shell']
        command += ['-run', '^TestZooKeeper(AgentNative|Native)']
        completed = subprocess.run(command, cwd=ROOT, env=env, stdout=subprocess.PIPE,
                                   stderr=subprocess.STDOUT, timeout=660 if args.public else 360)
        output = completed.stdout.decode(errors='replace')
        (base / 'acceptance.log').write_text(output)
        if completed.returncode:
            raise RuntimeError('Original ZooKeeper acceptance failed:\n' + output[-16000:])
        verify_acceptance(output, required)
        result['passed_tests'] = required
        if args.public:
            result['public_images'] = verify_public_images(base)
        if args.security:
            result['security_images'] = verify_security_images(base)
            result['security_receipts'] = verify_security_receipts(base)
            result['security_policies'] = {'digest': 'real protected node ACL; denied reads/writes',
                'tls': 'trusted CA and server IP SAN verification; TLS 1.2/1.3 allowed by server',
                'mtls': 'mandatory trusted client certificate; client hostname check disabled on server; server identity verified by client',
                'sasl': 'mandatory SASL DIGEST-MD5; ZooKeeper server/client JVM FIPS mode disabled'}
        if args.topology:
            result['topology_images'] = verify_topology_images(base)
            result['topology_receipts'] = verify_topology_receipts(base)
            result['ensemble_restored'] = verify_ensemble_restart(base, topology)
            result['topology_policies'] = {'ensemble': 'three official members; observed original TCP member stopped; quorum retained; original process/session/ephemeral owner retained',
                'secure_authentication': 'digest and SASL DIGEST-MD5 over TLS/mTLS; SASL JVM FIPS mode disabled'}
        if source_snapshot() != before:
            raise RuntimeError('Original source changed during acceptance')
        result['original_source_unchanged'] = True
        if nyadb_snapshot() != nyadb_before:
            raise RuntimeError('NyaDB source changed during native acceptance')
        result['nyadb_source_unchanged'] = True
        result['public_runtime_accepted'] = args.public_runtime
        result['public_entry_accepted'] = args.public
        result['security_accepted'] = args.security
        result['topology_accepted'] = args.topology
        if args.security:
            result['unverified'] = ['ensemble failover', 'SSH routes', 'other server versions', 'SASL Kerberos/GSSAPI', 'SASL Digest with server FIPS mode enabled']
        if args.topology:
            result['unverified'] = ['SSH/HTTP/proxy routes', 'other server versions', 'ensemble quorum loss and uncertain in-flight write failover', 'SASL Kerberos/GSSAPI', 'SASL Digest with server FIPS mode enabled', 'DBX browser/action depth']
        result['status'] = 'passed'
    except Exception as error:
        result['status'] = 'failed'
        result['public_runtime_accepted'] = False
        result['public_entry_accepted'] = False
        result['security_accepted'] = False
        result['topology_accepted'] = False
        result['error'] = str(error)
        raise
    finally:
        cleanup_errors = []
        try:
            result['process_cleanup'] = cleanup_processes(base)
            if result['process_cleanup']['forced_cleanup'] or not result['process_cleanup']['all_original_processes_exited']:
                cleanup_errors.append('Acceptance required forced native process cleanup')
        except Exception as error:
            cleanup_errors.append('Original process cleanup failed: ' + str(error))
        for mode, owned in containers:
            try:
                removed = cleanup_owned_container(base, mode, owned, token)
                result['anonymous_volumes_removed'] = result.get('anonymous_volumes_removed', 0) + removed
            except Exception as error:
                cleanup_errors.append('Owned ' + mode + ' container cleanup failed: ' + str(error))
        if containers:
            remaining = run(['docker', 'ps', '-aq', '--filter', 'label=nyadb.zookeeper.fixture=' + token]).decode().strip()
            if remaining:
                cleanup_errors.append('Owned ZooKeeper fixture remained after cleanup')
            else:
                result['container_removed'] = True
                result['containers_removed'] = len(containers)
        for network in networks:
            try:
                inspected = json.loads(run(['docker', 'network', 'inspect', network]))[0]
                if inspected.get('Labels', {}).get('nyadb.zookeeper.fixture') != token:
                    raise RuntimeError('Fixture network ownership changed')
                run(['docker', 'network', 'rm', network])
                remaining = run(['docker', 'network', 'ls', '-q', '--filter', 'label=nyadb.zookeeper.fixture=' + token]).decode().strip()
                if remaining:
                    raise RuntimeError('Owned network remained after cleanup')
                result['networks_removed'] = len(networks)
            except Exception as error:
                cleanup_errors.append('Owned network cleanup failed: ' + str(error))
        if cleanup_errors:
            result['status'] = 'failed'
            result['public_runtime_accepted'] = False
            result['public_entry_accepted'] = False
            result['security_accepted'] = False
            result['topology_accepted'] = False
            result['error'] = '\n'.join(filter(None, [result.get('error'), *cleanup_errors]))
        (base / 'verification.json').write_text(json.dumps(result, indent=2))
        if cleanup_errors:
            raise RuntimeError('\n'.join(cleanup_errors))
    print(json.dumps(result, indent=2), flush=True)


if __name__ == '__main__':
    main()
