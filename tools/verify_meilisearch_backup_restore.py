"""Owned Meilisearch artifact copy and real offline restore acceptance.

No backup files, credentials or API key material leave the private fixture folder.
The server directories below are explicitly configured by the owning verifier;
they are not paths discovered or promised by the application HTTP API.
"""
import hashlib
import json
from pathlib import Path
import re
import subprocess
import time
import urllib.request
import uuid


def verify_restores(identifier, nonce, folder, image, context, headers, expected, remove_owned, results=None):
    manifest = json.loads((folder/'backup-manifest.json').read_text())
    dump = manifest['dump']
    uid = dump['Artifact']['UID']
    if dump['State'] != 'succeeded' or dump['Artifact']['Kind'] != 'dump' or not re.fullmatch('[A-Za-z0-9_-]{1,128}', uid):
        raise RuntimeError('Invalid original dump task artifact')
    if results is None:
        results = []
    for kind, source in [('dump', '/meili_data/owned-backups/dumps/'+uid+'.dump'), ('snapshot', '/meili_data/owned-backups/snapshots/data.ms.snapshot')]:
        # Inspect ownership before reading an artifact from the original server.
        owned = json.loads(subprocess.check_output(['docker', 'inspect', identifier], text=True))[0]
        labels = owned['Config'].get('Labels') or {}
        if owned['Id'] != identifier or labels.get('nyadb.fixture') != 'meilisearch' or labels.get('nyadb.fixture.nonce') != nonce:
            raise RuntimeError('Original artifact ownership changed')
        artifact = folder/('restore-source.'+kind)
        subprocess.run(['docker', 'cp', identifier+':'+source, str(artifact)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
        artifact.chmod(0o600)
        if not artifact.is_file() or artifact.stat().st_size == 0:
            raise RuntimeError('Empty server artifact')
        restore_nonce = uuid.uuid4().hex
        restored = None
        result = dict(kind=kind, artifact_bytes=artifact.stat().st_size, artifact_sha256=hashlib.sha256(artifact.read_bytes()).hexdigest(), source_server_path=source, restored=False, fixture_cleaned=False)
        try:
            restored = subprocess.check_output(['docker', 'create', '--name', 'nyadb-meili-restore-'+restore_nonce[:12], '--label', 'nyadb.fixture=meilisearch', '--label', 'nyadb.fixture.nonce='+restore_nonce, '-p', '127.0.0.1::7700', '--mount', 'type=bind,src='+str(folder)+',dst=/owned,readonly', image, '/bin/meilisearch', '--config-file-path', '/owned/config.toml', '--import-'+kind, '/owned/'+artifact.name], text=True).strip()
            subprocess.run(['docker', 'start', restored], check=True, stdout=subprocess.DEVNULL)
            inspected = json.loads(subprocess.check_output(['docker', 'inspect', restored], text=True))[0]
            bindings = inspected['NetworkSettings']['Ports']['7700/tcp']
            if len(bindings) != 1 or bindings[0]['HostIp'] != '127.0.0.1':
                raise RuntimeError('Restore port ownership changed')
            endpoint = ('https' if context else 'http')+'://127.0.0.1:'+bindings[0]['HostPort']
            def read(path):
                with urllib.request.urlopen(urllib.request.Request(endpoint+path, headers=headers), context=context, timeout=5) as response:
                    return json.load(response)
            deadline = time.monotonic()+120
            while True:
                try:
                    identity = read('/version')
                    if identity == expected['identity']:
                        break
                except Exception:
                    pass
                if time.monotonic() >= deadline:
                    raise RuntimeError('Offline restore failed to reach original identity')
                time.sleep(.2)
            prefix = '/indexes/'+expected['index']
            index = read(prefix)
            if index['uid'] != expected['index'] or index['primaryKey'] != 'id':
                raise RuntimeError('Restored index identity differs')
            page = read(prefix+'/documents')
            if page['total'] != 1 or page['results'] != expected['documents']:
                raise RuntimeError('Restored exact documents differ')
            settings = read(prefix+'/settings')
            for name, value in expected['settings'].items():
                if settings[name] != value:
                    raise RuntimeError('Restored index settings differ: '+name)
            original = read('/tasks/'+str(expected['task_uid']))
            if original['uid'] != expected['task_uid'] or original['status'] != 'succeeded' or original['indexUid'] != expected['index']:
                raise RuntimeError('Restored completed task identity differs')
            if expected.get('key_uid'):
                key = read('/keys/'+expected['key_uid'])
                if key['uid'] != expected['key_uid'] or key['indexes'] != [expected['index']] or sorted(key['actions']) != sorted(expected['key_actions']):
                    raise RuntimeError('Restored API key policy differs')
                # Verify the restored delegated key can read original documents.
                delegated_headers = dict(headers, Authorization='Bearer '+key['key'])
                with urllib.request.urlopen(urllib.request.Request(endpoint+prefix+'/documents', headers=delegated_headers), context=context, timeout=5) as response:
                    if json.load(response)['results'] != expected['documents']:
                        raise RuntimeError('Restored delegated key cannot read original documents')
            result.update(restored=True, index_identity=True, exact_documents=True, settings=True, completed_task=True, delegated_key_policy=bool(expected.get('key_uid')), delegated_key_read=bool(expected.get('key_uid')), identity=identity)
        finally:
            if restored:
                remove_owned(restored, restore_nonce)
                result['fixture_cleaned'] = True
            artifact.unlink(missing_ok=True)
            result['private_artifact_cleaned'] = not artifact.exists()
            results.append(result)
    return results
