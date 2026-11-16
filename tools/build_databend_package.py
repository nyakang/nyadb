#!/usr/bin/env python3
"""Build an immutable original Databend agent/JDBC/JRE package.

Requires an explicit JDK 21+ and the pinned unchanged DBX source checkout.
Downloads have fixed SHA256 identities, including complete runtime POM/legal
provenance. Building a package does not register a public NyaDB provider.
"""
import argparse
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import platform
import re
import secrets
import shutil
import subprocess
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
PINS = ROOT / 'tools/databend_package_pins.json'
MAIN = 'com.dbx.agent.databend.DatabendAgent'
EPOCH = (2026, 11, 16, 0, 0, 0)


def sha(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def raw_sha(raw):
    return hashlib.sha256(raw).hexdigest()


def safe_path(name):
    return (isinstance(name, str) and name and not any(ord(c) < 32 for c in name) and
            '\\' not in name and ':' not in name and not name.startswith('/') and
            PurePosixPath(name).as_posix() == name and
            all(part not in ['', '.', '..'] for part in name.split('/')))


def fetch(item, cache, offline=False):
    if not safe_path(item['path']) or not re.fullmatch('[0-9a-f]{64}', item['sha256']):
        raise RuntimeError('Invalid fixed artifact identity')
    path = cache / item['path']
    if not path.exists():
        if offline:
            raise RuntimeError('Missing offline artifact: ' + item['path'])
        if not item['url'].startswith(('https://repo.maven.apache.org/maven2/',
                                       'https://raw.githubusercontent.com/')):
            raise RuntimeError('Unexpected fixed artifact source')
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_name(path.name + '.' + secrets.token_hex(8) + '.tmp')
        try:
            with urllib.request.urlopen(item['url'], timeout=45) as response:
                raw = response.read(128 * 1024 * 1024 + 1)
            if len(raw) > 128 * 1024 * 1024 or raw_sha(raw) != item['sha256']:
                raise RuntimeError('Downloaded artifact differs from its fixed SHA256')
            temporary.write_bytes(raw)
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)
    if not path.is_file() or sha(path) != item['sha256']:
        raise RuntimeError('Retained artifact differs from its fixed SHA256: ' + item['path'])
    return path


def verify_sources(dbx, pins):
    revision = subprocess.check_output(['git', '-C', str(dbx), 'rev-parse', 'HEAD'], text=True).strip()
    if revision != pins['dbx_revision']:
        raise RuntimeError('Original DBX source revision differs')
    expected = {item['path']: item['sha256'] for item in pins['dbx_source_files']}
    actual = {p.relative_to(dbx).as_posix() for directory in
              ['agents/common/src', 'agents/drivers/databend/src']
              for p in (dbx / directory).rglob('*') if p.is_file()}
    if actual != {name for name in expected if name.startswith(('agents/common/src/', 'agents/drivers/databend/src/'))}:
        raise RuntimeError('Original source/resource inventory differs')
    for name, digest in expected.items():
        if not safe_path(name) or sha(dbx / name) != digest:
            raise RuntimeError('Original DBX source/resource changed: ' + name)


def shaded_provenance(jdbc, dependencies, source, relocations):
    owners = defaultdict(list)
    archives = {coordinate: zipfile.ZipFile(path) for coordinate, path in dependencies.items()}
    try:
        for coordinate, archive in archives.items():
            for name in archive.namelist():
                if name.endswith('.class'):
                    owners[name].append(coordinate)
        with zipfile.ZipFile(source) as sources, zipfile.ZipFile(jdbc) as original:
            declarations = defaultdict(list)
            for name in sources.namelist():
                if not name.endswith('.java'):
                    continue
                text = sources.read(name).decode('utf-8')
                package = re.search(r'\bpackage\s+([\w.]+)\s*;', text)
                if package:
                    for declared in re.findall(r'\b(?:class|interface|enum|record)\s+(\w+)', text):
                        declarations[package[1].replace('.', '/') + '/' + declared].append(name)
            classes, natives = [], []
            for name in sorted(original.namelist()):
                if name.endswith('.class'):
                    before = name
                    prefix = ''
                    if name.startswith('META-INF/versions/'):
                        parts = name.split('/')
                        prefix, before = '/'.join(parts[:3]) + '/', '/'.join(parts[3:])
                    for relocated, upstream in relocations:
                        if before.startswith(relocated):
                            before = upstream + before[len(relocated):]
                            break
                    before = prefix + before
                    candidates = owners.get(before, [])
                    raw = original.read(name)
                    if len(candidates) > 1:
                        candidates = [c for c in candidates if archives[c].read(before) == raw]
                    if len(candidates) == 1:
                        coordinate = candidates[0]
                        dependency_raw = archives[coordinate].read(before)
                        classes.append({'path': name, 'coordinate': coordinate, 'dependency_member': before,
                                        'sha256': raw_sha(raw), 'dependency_member_sha256': raw_sha(dependency_raw),
                                        'exact_bytes': raw == dependency_raw})
                    elif not candidates and before == name and name.startswith('com/databend/jdbc/'):
                        declared = name.split('$', 1)[0].removesuffix('.class')
                        members = sorted(set(declarations.get(declared, [])))
                        if len(members) != 1:
                            raise RuntimeError('Original JDBC source mapping is missing or ambiguous: ' + name)
                        classes.append({'path': name, 'coordinate': 'com.databend:databend-jdbc:0.4.8',
                                        'source_member': members[0], 'sha256': raw_sha(raw),
                                        'source_member_sha256': raw_sha(sources.read(members[0]))})
                    else:
                        raise RuntimeError('Unreviewed shaded class ownership: ' + name)
                elif 'libzstd-jni-' in name:
                    coordinate = 'com.github.luben:zstd-jni:1.5.7-6'
                    raw = original.read(name)
                    if raw != archives[coordinate].read(name):
                        raise RuntimeError('Original native zstd bytes differ')
                    natives.append({'path': name, 'coordinate': coordinate, 'sha256': raw_sha(raw), 'exact_bytes': True})
            return {'classes': classes, 'native_binaries': natives, 'relocations': relocations}
    finally:
        for archive in archives.values():
            archive.close()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def canonical_archive(package, destination):
    with zipfile.ZipFile(destination, 'x', compression=zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(package.rglob('*')):
            if path.is_file():
                info = zipfile.ZipInfo(path.relative_to(package).as_posix(), EPOCH)
                info.create_system = 3
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = (0o100755 if path.stat().st_mode & 0o111 else 0o100644) << 16
                archive.writestr(info, path.read_bytes())


def verify_package(package):
    manifest = json.loads((package / 'manifest.json').read_text())
    declared = set()
    for item in manifest['files']:
        name = item['path']
        if not safe_path(name) or name in declared or not (package / name).resolve().is_relative_to(package.resolve()):
            raise RuntimeError('Ambiguous or escaped package artifact')
        declared.add(name)
        if sha(package / name) != item['sha256']:
            raise RuntimeError('Package artifact changed: ' + name)
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != declared | {'manifest.json'}:
        raise RuntimeError('Undeclared or missing package payload')
    if manifest['key'] != 'databend' or manifest['runtime'] != 'java' or manifest['main_class'] != MAIN:
        raise RuntimeError('Original managed process identity differs')
    return manifest


def build(dbx, java, base, version, cache, offline=False):
    pins = json.loads(PINS.read_text())
    if pins['format'] != 1 or not re.fullmatch('[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}', version):
        raise RuntimeError('Invalid package version or pin format')
    verify_sources(dbx, pins)
    if platform.system() != 'Linux' or platform.machine() not in ['x86_64', 'aarch64']:
        raise RuntimeError('This builder currently requires a native Linux JDK')
    jdk = java.resolve().parent.parent
    jdk_files = [jdk / 'release', *(jdk / 'bin' / name for name in ['java', 'javac', 'jar', 'jlink'])]
    jdk_files += sorted((jdk / 'jmods').glob('*.jmod'))
    identity = {p.relative_to(jdk).as_posix(): sha(p) for p in jdk_files}
    base.mkdir(parents=True, exist_ok=False)
    print('Building original package: ' + str(base), flush=True)
    with ThreadPoolExecutor(max_workers=6) as pool:
        paths = list(pool.map(lambda item: fetch(item, cache, offline), pins['files']))
    acquired = dict(zip((item['path'] for item in pins['files']), paths))
    jars = {item['coordinate']: acquired[item['path']] for item in pins['files'] if item['extension'] == 'jar'}
    jdbc = jars['com.databend:databend-jdbc:0.4.8']
    source = next(acquired[item['path']] for item in pins['files'] if item['extension'] == 'sources')
    launch = [jars[c] for c in pins['launch_coordinates']]
    dependencies = {c: p for c, p in jars.items() if c not in pins['launch_coordinates']}
    mapping = shaded_provenance(jdbc, dependencies, source, pins['relocations'])
    classes = base / 'classes'
    classes.mkdir()
    sources = sorted(dbx / item['path'] for item in pins['dbx_source_files']
                     if '/src/main/java/' in item['path'] and item['path'].endswith('.java'))
    argfile = base / 'javac-sources.txt'
    argfile.write_text('\n'.join('"' + str(p).replace('\\', '\\\\').replace('"', '\\"') + '"' for p in sources) + '\n')
    with (base / 'compile.log').open('w') as log:
        subprocess.run([str(jdk / 'bin/javac'), '--release', '21', '-encoding', 'UTF-8', '-cp',
                        os.pathsep.join(map(str, launch)), '-d', str(classes), '@' + str(argfile)],
                       check=True, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    shutil.copytree(dbx / 'agents/common/src/main/resources', classes, dirs_exist_ok=True)
    for name in ['LICENSE', 'NOTICE']:
        shutil.copy2(dbx / name, classes / ('DBX-' + name))
    package = base / 'package'
    package.mkdir()
    agent = package / 'dbx-agent-databend.jar'
    subprocess.run([str(jdk / 'bin/jar'), '--create', '--date=2026-11-16T00:00:00Z', '--file', str(agent),
                    '--main-class', MAIN, '-C', str(classes), '.'], check=True, timeout=60)
    with (base / 'jlink.log').open('w') as log:
        subprocess.run([str(jdk / 'bin/jlink'), '--add-modules', 'java.se,jdk.crypto.ec,jdk.unsupported,jdk.security.auth',
                        '--strip-debug', '--no-header-files', '--no-man-pages', '--output', str(package / 'jre')],
                       check=True, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    for path in (package / 'jre').rglob('*'):
        if path.is_symlink():
            if not path.resolve().is_relative_to((package / 'jre').resolve()):
                raise RuntimeError('Linked runtime legal file escaped its image')
            raw = path.read_bytes()
            path.unlink()
            path.write_bytes(raw)
    for path in launch:
        shutil.copy2(path, package / path.name)
    for name in ['LICENSE', 'NOTICE']:
        shutil.copy2(dbx / name, package / ('DBX-' + name))
    legal = package / 'legal'
    legal.mkdir()
    provenance = package / 'provenance'
    provenance.mkdir()
    write_json(provenance / 'pins.json', pins)
    write_json(provenance / 'shaded-members.json', mapping)
    shutil.copy2(source, provenance / source.name)
    legal_mapping = []
    for item in pins['files']:
        path = acquired[item['path']]
        if item['extension'] == 'pom':
            target = provenance / 'maven' / item['path']
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
        elif item['extension'] == 'legal':
            target = legal / Path(item['path']).name
            shutil.copy2(path, target)
            legal_mapping.append({'file': target.relative_to(package).as_posix(), 'coordinate': item['coordinate'],
                                  'source_url': item['url'], 'sha256': item['sha256']})
        elif item['extension'] == 'jar':
            with zipfile.ZipFile(path) as archive:
                component = next(c for c in pins['components'] if c['coordinate'] == item['coordinate'])
                for index, entry in enumerate(component['embedded_legal']):
                    raw = archive.read(entry['member'])
                    if raw_sha(raw) != entry['sha256']:
                        raise RuntimeError('Original embedded license changed')
                    target = legal / (path.stem + '-' + str(index) + '.txt')
                    target.write_bytes(raw)
                    legal_mapping.append({'file': target.relative_to(package).as_posix(), 'coordinate': item['coordinate'],
                                          'jar_sha256': item['sha256'], 'member': entry['member'], 'sha256': entry['sha256']})
    write_json(provenance / 'legal-members.json', legal_mapping)
    for item in pins['dbx_source_files']:
        target = provenance / 'dbx' / item['path']
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(dbx / item['path'], target)
    source_provenance = {'format': 1, 'dbx_revision': pins['dbx_revision'], 'source_files': pins['dbx_source_files'],
                         'compiled_source_files': [p.relative_to(dbx).as_posix() for p in sources],
                         'dependencies': pins['files'], 'components': pins['components'], 'jdk_files': identity,
                         'agent_sha256': sha(agent), 'builder_sha256': sha(Path(__file__)), 'pins_sha256': sha(PINS),
                         'public_provider_registered': False, 'original_driver_modified': False}
    write_json(package / 'sources.json', source_provenance)
    verify_sources(dbx, pins)
    if identity != {p.relative_to(jdk).as_posix(): sha(p) for p in jdk_files}:
        raise RuntimeError('Original build JDK changed during construction')
    files = [{'path': p.relative_to(package).as_posix(), 'sha256': sha(p), 'executable': bool(p.stat().st_mode & 0o111)}
             for p in sorted(package.rglob('*')) if p.is_file()]
    licenses = ['DBX-LICENSE', 'DBX-NOTICE'] + [f['path'] for f in files if f['path'].startswith(('legal/', 'jre/legal/'))]
    if len(licenses) > 256:
        raise RuntimeError('Complete original license list exceeds the managed manifest limit')
    manifest = {'format': 1, 'key': 'databend', 'version': version,
                'platform': 'linux-' + ('amd64' if platform.machine() == 'x86_64' else 'arm64'),
                'runtime': 'java', 'executable': 'jre/bin/java', 'main_class': MAIN,
                'classpath': [agent.name] + [p.name for p in launch], 'profiles': ['databend'],
                'files': files, 'licenses': licenses, 'provenance': 'sources.json'}
    write_json(package / 'manifest.json', manifest)
    verify_package(package)
    archive = base / 'databend.zip'
    canonical_archive(package, archive)
    write_json(base / 'build.json', {'archive': archive.name, 'archive_sha256': sha(archive),
                                    'manifest_sha256': sha(package / 'manifest.json'), 'files': len(files),
                                    'licenses': len(licenses), 'shaded_classes': len(mapping['classes']),
                                    'exact_native_binaries': len(mapping['native_binaries'])})
    return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dbx', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--java', type=Path, default=shutil.which('java'))
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cache', type=Path, default=ROOT / 'build/databend/package-cache')
    parser.add_argument('--version', default='jdbc-0.4.8-agent-0.1.82')
    parser.add_argument('--offline', action='store_true')
    args = parser.parse_args()
    archive = build(args.dbx.resolve(), args.java.resolve(), args.output.resolve(), args.version, args.cache.resolve(), args.offline)
    print('Original immutable archive SHA256: ' + sha(archive), flush=True)


if __name__ == '__main__':
    main()
