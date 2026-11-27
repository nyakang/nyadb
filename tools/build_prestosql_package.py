#!/usr/bin/env python3
"""Package the unchanged DBX JDBC plugin and PrestoSQL 350 driver.

Requires the pinned DBX checkout, its original shadowJar, and a native JDK 21.
Recompilation verifies the original plugin classes; the shipped artifact stays
the original shaded JAR, including its service merges and multi-release files.
This builds a private runtime package, not a public provider registration.
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
PINS = ROOT / 'tools/prestosql_package_pins.json'
MAIN = 'app.dbx.jdbc.DbxJdbcPlugin'
DRIVER = 'io.prestosql:presto-jdbc:350'


def sha(path):
    with path.open('rb') as stream:
        return hashlib.file_digest(stream, 'sha256').hexdigest()


def raw_sha(raw):
    return hashlib.sha256(raw).hexdigest()


def write_json(path, value):
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + '\n')


def safe_path(name):
    return (isinstance(name, str) and name and not any(ord(c) < 32 or ord(c) == 127 for c in name)
            and '\\' not in name and ':' not in name and not name.startswith('/')
            and PurePosixPath(name).as_posix() == name
            and all(part not in ['', '.', '..'] for part in name.split('/')))


def fetch(item, cache, offline=False):
    if not safe_path(item['path']) or not re.fullmatch('[0-9a-f]{64}', item['sha256']):
        raise RuntimeError('Invalid fixed artifact identity')
    path = cache / item['path']
    if not path.resolve().is_relative_to(cache.resolve()):
        raise RuntimeError('Artifact cache path escaped')
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
        raise RuntimeError('Original DBX revision differs')
    expected = {item['path']: item['sha256'] for item in pins['dbx_source_files']}
    actual = {p.relative_to(dbx).as_posix() for p in (dbx / 'plugins/jdbc/src').rglob('*') if p.is_file()}
    if actual != {name for name in expected if name.startswith('plugins/jdbc/src/')}:
        raise RuntimeError('Original source/resource inventory differs')
    for name, digest in expected.items():
        if not safe_path(name) or sha(dbx / name) != digest:
            raise RuntimeError('Original DBX source/resource changed: ' + name)


def plugin_provenance(plugin, dependencies, classes, expected):
    actual = {p.relative_to(classes).as_posix(): sha(p) for p in classes.rglob('*.class')}
    if actual != expected:
        raise RuntimeError('Recompiled original plugin classes differ')
    owners = defaultdict(list)
    for name, digest in actual.items():
        owners[name].append({'source': 'original-plugin', 'sha256': digest})
    for coordinate, path in sorted(dependencies.items()):
        with zipfile.ZipFile(path) as archive:
            for name in archive.namelist():
                if name.endswith('.class'):
                    owners[name].append({'coordinate': coordinate, 'sha256': raw_sha(archive.read(name))})
    rows = []
    with zipfile.ZipFile(plugin) as original:
        for name in sorted(original.namelist()):
            if not name.endswith('.class'):
                continue
            digest = raw_sha(original.read(name))
            matched = [item for item in owners[name] if item['sha256'] == digest]
            if len(matched) != 1:
                raise RuntimeError('Original plugin class ownership is missing or ambiguous: ' + name)
            rows.append({'member': name, **matched[0], 'exact_bytes': True})
    return rows


def jdbc_provenance(jdbc, dependencies, source, relocations):
    owners = defaultdict(list)
    for coordinate, path in sorted(dependencies.items()):
        with zipfile.ZipFile(path) as archive:
            for name in archive.namelist():
                if name.endswith('.class'):
                    owners[name].append({'coordinate': coordinate, 'member': name,
                                         'sha256': raw_sha(archive.read(name))})
    rows = []
    with zipfile.ZipFile(source) as sources, zipfile.ZipFile(jdbc) as original:
        for name in sorted(original.namelist()):
            if not name.endswith('.class'):
                continue
            before = name
            for relocated, upstream in relocations:
                if before.startswith(relocated):
                    before = upstream + before[len(relocated):]
                    break
            candidates = list(owners[before])
            if before.startswith('com/google/common/'):
                candidates += owners[before.replace('com/google/common/', 'com/google/thirdparty/', 1)]
            row = {'member': name, 'sha256': raw_sha(original.read(name))}
            own = ((name.startswith('io/prestosql/jdbc/') and '/$internal/' not in name)
                   or name.startswith('com/facebook/presto/jdbc/'))
            source_name = name.split('$', 1)[0].removesuffix('.class') + '.java'
            if own and source_name in sources.namelist() and not candidates:
                row.update(coordinate=DRIVER, source_member=source_name,
                           source_member_sha256=raw_sha(sources.read(source_name)))
            elif len(candidates) == 1:
                row['upstream'] = candidates[0]
                row['exact_bytes'] = row['sha256'] == candidates[0]['sha256']
            else:
                raise RuntimeError('Original JDBC class ownership is missing or ambiguous: ' + name)
            rows.append(row)
    return {'classes': rows, 'relocations': relocations,
            'boundary': 'Relocated members map by original class identity; transformed bytes are not asserted identical.'}


def canonical_archive(package, destination):
    with zipfile.ZipFile(destination, 'x', compression=zipfile.ZIP_DEFLATED) as archive:
        for path in sorted(package.rglob('*')):
            if path.is_file():
                info = zipfile.ZipInfo(path.relative_to(package).as_posix(), (2026, 11, 28, 0, 0, 0))
                info.create_system = 3
                info.compress_type = zipfile.ZIP_DEFLATED
                info.external_attr = (0o100755 if path.stat().st_mode & 0o111 else 0o100644) << 16
                archive.writestr(info, path.read_bytes())


def verify_package(package):
    manifest = json.loads((package / 'manifest.json').read_text())
    declared = set()
    folded = set()
    for item in manifest['files']:
        name = item['path']
        path = package / name
        if (not safe_path(name) or name.lower() in folded or path.is_symlink()
                or not path.resolve().is_relative_to(package.resolve())):
            raise RuntimeError('Ambiguous or escaped package artifact')
        declared.add(name)
        folded.add(name.lower())
        if sha(path) != item['sha256'] or bool(path.stat().st_mode & 0o111) != item['executable']:
            raise RuntimeError('Package artifact changed: ' + name)
    actual = {p.relative_to(package).as_posix() for p in package.rglob('*') if p.is_file()}
    if actual != declared | {'manifest.json'}:
        raise RuntimeError('Undeclared or missing package payload')
    if (manifest['key'] != 'prestosql' or manifest['runtime'] != 'java' or manifest['main_class'] != MAIN
            or manifest['classpath'] != ['dbx-jdbc-plugin.jar']
            or manifest['jdbc'] != {'class': 'io.prestosql.jdbc.PrestoDriver', 'classpath': ['jdbc/presto-jdbc-350.jar']}):
        raise RuntimeError('Original managed process identity differs')
    for name in [manifest['executable'], manifest['provenance'], *manifest['licenses'],
                 *manifest['classpath'], *manifest['jdbc']['classpath']]:
        if name not in declared:
            raise RuntimeError('Manifest references a missing artifact')
    return manifest


def build(dbx, plugin, java, base, version, cache, offline=False):
    pins = json.loads(PINS.read_text())
    if pins['format'] != 1 or not re.fullmatch('[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}', version):
        raise RuntimeError('Invalid version or pin format')
    verify_sources(dbx, pins)
    if sha(plugin) != pins['plugin_sha256']:
        raise RuntimeError('Original shaded plugin differs')
    if platform.system() != 'Linux' or platform.machine() not in ['x86_64', 'aarch64']:
        raise RuntimeError('This builder currently requires a native Linux JDK')
    jdk = java.resolve().parent.parent
    jdk_files = [jdk / 'release', *(jdk / 'bin' / name for name in ['java', 'javac', 'jlink'])]
    jdk_files += sorted((jdk / 'jmods').glob('*.jmod'))
    identity = {p.relative_to(jdk).as_posix(): sha(p) for p in jdk_files}
    base.mkdir(parents=True, exist_ok=False)
    with ThreadPoolExecutor(max_workers=6) as pool:
        paths = list(pool.map(lambda item: fetch(item, cache, offline), pins['files']))
    acquired = dict(zip((item['path'] for item in pins['files']), paths))
    jars = {item['coordinate']: acquired[item['path']] for item in pins['files'] if item['extension'] == 'jar'}
    dependencies = {item['coordinate']: jars[item['coordinate']] for item in pins['components']
                    if 'plugin-runtime' in item['roles']}
    shaded = {item['coordinate']: jars[item['coordinate']] for item in pins['components']
              if 'jdbc-shaded' in item['roles']}
    sources = sorted(dbx / item['path'] for item in pins['dbx_source_files']
                     if '/src/main/java/' in item['path'] and item['path'].endswith('.java'))
    classes = base / 'classes'
    classes.mkdir()
    argfile = base / 'javac-sources.txt'
    argfile.write_text('\n'.join('"' + str(p).replace('\\', '\\\\').replace('"', '\\"') + '"' for p in sources) + '\n')
    with (base / 'compile.log').open('w') as log:
        subprocess.run([str(jdk / 'bin/javac'), '-g', '--release', '21', '-encoding', 'UTF-8', '-cp',
                        os.pathsep.join(map(str, dependencies.values())), '-d', str(classes), '@' + str(argfile)],
                       check=True, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    plugin_mapping = plugin_provenance(plugin, dependencies, classes, pins['original_plugin_classes'])
    source = next(acquired[item['path']] for item in pins['files'] if item['extension'] == 'sources')
    jdbc_mapping = jdbc_provenance(jars[DRIVER], shaded, source, pins['jdbc_relocations'])
    package = base / 'package'
    package.mkdir()
    with (base / 'jlink.log').open('w') as log:
        subprocess.run([str(jdk / 'bin/jlink'), '--add-modules', 'java.se,jdk.crypto.ec,jdk.unsupported,jdk.security.auth',
                        '--strip-debug', '--no-header-files', '--no-man-pages', '--output', str(package / 'jre')],
                       check=True, stdout=log, stderr=subprocess.STDOUT, timeout=180)
    for path in (package / 'jre').rglob('*'):
        if path.is_symlink():
            if not path.resolve().is_relative_to((package / 'jre').resolve()):
                raise RuntimeError('Linked runtime file escaped its image')
            raw = path.read_bytes()
            path.unlink()
            path.write_bytes(raw)
    shutil.copy2(plugin, package / 'dbx-jdbc-plugin.jar')
    (package / 'jdbc').mkdir()
    shutil.copy2(jars[DRIVER], package / 'jdbc/presto-jdbc-350.jar')
    for name in ['LICENSE', 'NOTICE']:
        shutil.copy2(dbx / name, package / ('DBX-' + name))
    legal = package / 'legal'
    legal.mkdir()
    provenance = package / 'provenance'
    provenance.mkdir()
    write_json(provenance / 'pins.json', pins)
    write_json(provenance / 'plugin-members.json', plugin_mapping)
    write_json(provenance / 'jdbc-members.json', jdbc_mapping)
    legal_mapping = []
    for item in pins['files']:
        path = acquired[item['path']]
        if item['extension'] in ['pom', 'source-pom', 'sources']:
            target = provenance / 'upstream' / item['path']
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(path, target)
        elif item['extension'] == 'legal':
            target = legal / Path(item['path']).name
            shutil.copy2(path, target)
            legal_mapping.append({'file': target.relative_to(package).as_posix(),
                                  'source_url': item['url'], 'sha256': item['sha256']})
        elif item['extension'] == 'jar':
            with zipfile.ZipFile(path) as archive:
                component = next(c for c in pins['components'] if c['coordinate'] == item['coordinate'])
                for index, entry in enumerate(component['embedded_legal']):
                    raw = archive.read(entry['member'])
                    if raw_sha(raw) != entry['sha256']:
                        raise RuntimeError('Original embedded license changed')
                    target = legal / (path.stem + '-' + str(index) + '.txt')
                    if target.exists():
                        raise RuntimeError('Ambiguous component license filename')
                    target.write_bytes(raw)
                    legal_mapping.append({'file': target.relative_to(package).as_posix(), 'coordinate': item['coordinate'],
                                          'jar_sha256': item['sha256'], 'member': entry['member'], 'sha256': entry['sha256']})
    write_json(provenance / 'legal-members.json', legal_mapping)
    for item in pins['dbx_source_files']:
        target = provenance / 'dbx' / item['path']
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copy2(dbx / item['path'], target)
    write_json(package / 'sources.json', {
        'format': 1, 'dbx_revision': pins['dbx_revision'], 'source_files': pins['dbx_source_files'],
        'dependencies': pins['files'], 'components': pins['components'], 'jdk_files': identity,
        'plugin_sha256': sha(plugin), 'jdbc_sha256': sha(jars[DRIVER]),
        'builder_sha256': sha(Path(__file__)), 'pins_sha256': sha(PINS),
        'original_plugin_modified': False, 'original_driver_modified': False,
        'public_provider_registered': False,
        'plugin_verification': 'Pinned original shadowJar; every plugin class recompiles identically and every dependency class maps to exact original bytes.'})
    verify_sources(dbx, pins)
    if identity != {p.relative_to(jdk).as_posix(): sha(p) for p in jdk_files}:
        raise RuntimeError('Original build JDK changed during construction')
    if sha(package / 'dbx-jdbc-plugin.jar') != pins['plugin_sha256']:
        raise RuntimeError('Original plugin changed during construction')
    for item in pins['files']:
        if sha(acquired[item['path']]) != item['sha256']:
            raise RuntimeError('Pinned artifact changed during construction')
    files = [{'path': p.relative_to(package).as_posix(), 'sha256': sha(p), 'executable': bool(p.stat().st_mode & 0o111)}
             for p in sorted(package.rglob('*')) if p.is_file()]
    licenses = ['DBX-LICENSE', 'DBX-NOTICE'] + [f['path'] for f in files if f['path'].startswith(('legal/', 'jre/legal/'))]
    if len(licenses) > 256:
        raise RuntimeError('Complete original license list exceeds the managed manifest limit')
    manifest = {'format': 1, 'key': 'prestosql', 'version': version,
                'platform': 'linux-' + ('amd64' if platform.machine() == 'x86_64' else 'arm64'),
                'runtime': 'java', 'executable': 'jre/bin/java', 'main_class': MAIN,
                'classpath': ['dbx-jdbc-plugin.jar'], 'profiles': ['prestosql'],
                'jdbc': {'class': 'io.prestosql.jdbc.PrestoDriver', 'classpath': ['jdbc/presto-jdbc-350.jar']},
                'files': files, 'licenses': licenses, 'provenance': 'sources.json'}
    write_json(package / 'manifest.json', manifest)
    verify_package(package)
    archive = base / 'prestosql.zip'
    canonical_archive(package, archive)
    write_json(base / 'build.json', {'archive': archive.name, 'archive_sha256': sha(archive),
                                    'manifest_sha256': sha(package / 'manifest.json'), 'files': len(files),
                                    'licenses': len(licenses), 'plugin_classes': len(plugin_mapping),
                                    'jdbc_classes': len(jdbc_mapping['classes'])})
    return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dbx', type=Path, default=ROOT / 'temp/dbx')
    parser.add_argument('--plugin', type=Path, required=True)
    parser.add_argument('--java', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--cache', type=Path, default=ROOT / 'build/prestosql/package-cache')
    parser.add_argument('--version', default='jdbc-350-plugin-0.1.57')
    parser.add_argument('--offline', action='store_true')
    args = parser.parse_args()
    archive = build(args.dbx.resolve(), args.plugin.resolve(), args.java.resolve(), args.output.resolve(),
                    args.version, args.cache.resolve(), args.offline)
    print('Original immutable archive SHA256: ' + sha(archive), flush=True)


if __name__ == '__main__':
    main()
