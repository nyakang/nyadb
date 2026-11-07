#!/usr/bin/env python3
"""Verify an owned native Doris FE/BE fixture and remove only its original ID.

On hosts without AVX2, set NYADB_DORIS_BE_LIB to the extracted be/lib directory
of the official, signature-verified 4.1.3 noavx2 release (see the Doris report).
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
IMAGE = 'apache/doris@sha256:4d3aef8f4fe63f6e32b493b4fd7b2a682d19e2c800d1d812131e67aaba0d212a'

def remove_owned(identifier, nonce):
    if not isinstance(identifier,str) or not re.fullmatch('[0-9a-f]{64}',identifier):
        raise RuntimeError('invalid original Doris fixture ID')
    result = subprocess.run(['docker','inspect',identifier],capture_output=True,text=True)
    if result.returncode:
        return
    obj = json.loads(result.stdout)[0]
    labels = obj.get('Config',{}).get('Labels') or {}
    if obj.get('Id') != identifier or labels.get('nyadb.fixture') != 'doris' or labels.get('nyadb.fixture.nonce') != nonce:
        raise RuntimeError('original fixture ownership changed; refuse cleanup')
    subprocess.run(['docker','rm','-f',identifier],check=True,stdout=subprocess.DEVNULL)

def verify(plain=False, hold=False, race=False):
    root = ROOT
    image = IMAGE
    base = root / 'temp/doris'
    output = root / 'build/doris'
    base.mkdir(parents=True,exist_ok=True)
    output.mkdir(parents=True,exist_ok=True)
    nonce = uuid.uuid4().hex
    identifier = None
    def terminate(signum, _frame):
        raise SystemExit(128 + signum)
    signal.signal(signal.SIGTERM, terminate)
    signal.signal(signal.SIGINT, terminate)
    subprocess.run(['docker','image','inspect',image],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    native_lib = os.environ.get('NYADB_DORIS_BE_LIB')
    extra = []
    if native_lib:
        lib = Path(native_lib).resolve(strict=True)
        assert lib.is_dir() and (lib/'doris_be').is_file()
        extra = ['--mount','type=bind,source='+str(lib)+',target=/opt/apache-doris/be/lib,readonly']
    tls = not plain
    with tempfile.TemporaryDirectory(prefix='fixture-',dir=base) as temporary:
        folder = Path(temporary)
        folder.chmod(0o700)
        secret = 'Nya' + secrets.token_hex(20) + '9'
        password_file = folder / 'password'
        password_file.write_text(secret)
        password_file.chmod(0o600)
        if tls:
            ssl = folder / 'ssl'
            ssl.mkdir(mode=0o700)
            def openssl(*args):
                subprocess.run(['openssl',*args],check=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            openssl('req','-x509','-newkey','rsa:2048','-nodes','-days','2','-subj','/CN=NyaDB disposable Doris CA',
                '-keyout',str(ssl/'ca-key.pem'),'-out',str(ssl/'ca-cert.pem'),
                '-addext','basicConstraints=critical,CA:TRUE','-addext','keyUsage=critical,keyCertSign,cRLSign')
            openssl('req','-new','-newkey','rsa:2048','-nodes','-subj','/CN=localhost',
                '-keyout',str(ssl/'server-key.pem'),'-out',str(ssl/'server.csr'))
            (ssl/'extensions').write_text('basicConstraints=critical,CA:FALSE\nkeyUsage=critical,digitalSignature,keyEncipherment\nextendedKeyUsage=serverAuth\nsubjectAltName=DNS:localhost,IP:127.0.0.1\n')
            openssl('x509','-req','-in',str(ssl/'server.csr'),'-CA',str(ssl/'ca-cert.pem'),'-CAkey',str(ssl/'ca-key.pem'),
                '-CAcreateserial','-days','2','-sha256','-extfile',str(ssl/'extensions'),'-out',str(ssl/'server-cert.pem'))
            openssl('pkcs12','-export','-inkey',str(ssl/'server-key.pem'),'-in',str(ssl/'server-cert.pem'),
                '-certfile',str(ssl/'ca-cert.pem'),'-name','doris-server','-passout','pass:doris','-out',str(ssl/'server_certificate.p12'))
            openssl('pkcs12','-export','-nokeys','-in',str(ssl/'ca-cert.pem'),'-name','nyadb-ca',
                '-jdktrust','anyExtendedKeyUsage','-passout','pass:doris','-out',str(ssl/'ca_certificate.p12'))
            for file in ssl.iterdir():
                file.chmod(0o600)
            extra += ['-e','FE_CONFIG_EXTRA=enable_ssl=true\nmysql_ssl_default_ca_certificate=/nyadb-source/ssl/ca_certificate.p12\nmysql_ssl_default_server_certificate=/nyadb-source/ssl/server_certificate.p12\n']
        database = 'nyadb_doris_' + nonce[:12]
        try:
            identifier = subprocess.check_output(['docker','create','--name','nyadb-doris-it-'+nonce[:12],
                '--label','nyadb.fixture=doris','--label','nyadb.fixture.nonce='+nonce,'--memory=8g','--cpus=4','--shm-size=1g',
                '--ulimit','nofile=65536:65536','-p','127.0.0.1::9030','-p','127.0.0.1::8030','-p','127.0.0.1::8040',
                '--mount','type=bind,source='+str(folder)+',target=/nyadb-source,readonly',*extra,image],text=True).strip()
            assert re.fullmatch('[0-9a-f]{64}',identifier)
            subprocess.run(['docker','start',identifier],check=True,stdout=subprocess.DEVNULL)
            deadline = time.monotonic() + 150
            while True:
                obj = json.loads(subprocess.check_output(['docker','inspect',identifier],text=True))[0]
                if obj['State'].get('Health',{}).get('Status') == 'healthy':
                    break
                if not obj['State']['Running'] or time.monotonic() >= deadline:
                    raise RuntimeError('Doris FE/BE readiness failed; see captured owned server log')
                time.sleep(1)
            command = ['docker','exec','-i',identifier,'mysql','--batch','--raw','--skip-column-names','--connect-timeout=5','-h127.0.0.1','-P9030','-uroot']
            identity = subprocess.run(command,input='SELECT VERSION(),@@version_comment; SHOW FRONTENDS; SHOW BACKENDS;\n',capture_output=True,text=True,timeout=15)
            if identity.returncode or 'doris-4.1.3' not in identity.stdout:
                raise RuntimeError('native SQL does not prove selected Doris product/kernel')
            (output / ('identity-'+nonce+'.txt')).write_text(identity.stdout)
            bootstrap = "CREATE USER 'nyadb_it'@'%' IDENTIFIED BY '"+secret+"'; GRANT ADMIN_PRIV ON *.*.* TO 'nyadb_it'@'%'; CREATE DATABASE `"+database+"`;\n"
            result = subprocess.run(command,input=bootstrap,capture_output=True,text=True,timeout=15)
            if result.returncode:
                (output / ('bootstrap-error-'+nonce+'.log')).write_text((result.stdout+result.stderr).replace(secret,'[redacted]'))
                raise RuntimeError('owned native authentication/database bootstrap failed')
            # The image's health check can pass before FE receives the BE disk
            # report. Admit the fixture only after storage-backed CREATE/SELECT
            # works, rather than retrying any acceptance-test user command.
            readiness = "CREATE TABLE IF NOT EXISTS `"+database+"`.`__nyadb_ready` (ID BIGINT) DUPLICATE KEY(ID) DISTRIBUTED BY HASH(ID) BUCKETS 1 PROPERTIES ('replication_num'='1'); SELECT COUNT(*) FROM `"+database+"`.`__nyadb_ready`;\n"
            deadline = time.monotonic() + 150
            while True:
                ready = subprocess.run(command,input=readiness,capture_output=True,text=True,timeout=15)
                if ready.returncode == 0 and ready.stdout.strip() == '0':
                    break
                if time.monotonic() >= deadline:
                    (output / ('readiness-error-'+nonce+'.log')).write_text((ready.stdout+ready.stderr).replace(secret,'[redacted]'))
                    raise RuntimeError('native Doris storage did not become ready')
                time.sleep(1)
            ports = {str(port):subprocess.check_output(['docker','port',identifier,str(port)+'/tcp'],text=True).strip().rsplit(':',1)[1] for port in [9030,8030,8040]}
            private = folder / 'probe.json'
            private.write_text(json.dumps({'endpoint':'127.0.0.1:'+ports['9030'],'username':'nyadb_it','password_file':str(password_file),'database':database,'catalog':'internal',
                'ca_file':str(ssl/'ca-cert.pem') if tls else '', 'server_name':'localhost' if tls else ''}))
            private.chmod(0o600)
            (output / ('owned-fixture-'+nonce+'.json')).write_text(json.dumps({'id':identifier,'nonce':nonce,'image':image,'folder':str(folder),'probe':str(private),'ports':ports,'database':database,'tls':tls,'native_library_directory':native_lib},indent=2)+'\n')
            print('Doris native FE/BE verified; private probe:',private,flush=True)
            if hold:
                while True:
                    time.sleep(1)
            else:
                env = os.environ.copy()
                env.update({'NYADB_DORIS_IT':'1','NYADB_DORIS_CONFIG':str(private),
                    'NYADB_DORIS_VISUAL_DIR':str(output / ('visual-'+nonce))})
                packages = ['./internal/services/doris','./internal/services/dbruntime','./internal/ui/shell']
                args = ['go','test','-count=1','-v','-run','DorisNative',*(['-race'] if race else []),*packages]
                with (output / ('acceptance-'+nonce+'.log')).open('w') as log:
                    result = subprocess.run(args,cwd=root,env=env,stdout=log,stderr=subprocess.STDOUT,timeout=300)
                if result.returncode:
                    raise RuntimeError('Doris native acceptance failed; see captured test log')
                print('Doris native acceptance passed',flush=True)
        finally:
            if identifier:
                logs = subprocess.run(['docker','logs',identifier],capture_output=True,text=True)
                (output / ('server-'+nonce+'.log')).write_text((logs.stdout+logs.stderr).replace(secret,'[redacted]'))
                remove_owned(identifier, nonce)

if __name__ == '__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--plain',action='store_true',help='use a plaintext fixture instead of verified TLS')
    parser.add_argument('--hold',action='store_true',help='hold the owned fixture for manual local probes')
    parser.add_argument('--race',action='store_true',help='run native lifecycle checks with the Go race detector')
    args=parser.parse_args()
    verify(plain=args.plain,hold=args.hold,race=args.race)
