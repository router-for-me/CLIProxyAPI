#!/usr/bin/env python3
"""Test real monolith packages over SSH in disposable, loopback-only Docker resources."""
import argparse
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import subprocess
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parents[2]


def run(*args, check=True, timeout=120, env=None, input=None):
    result = subprocess.run(args, cwd=ROOT, env=env, input=input, text=True,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f'{args[0]} {args[1] if len(args)>1 else ""} failed:\n{result.stdout[-6000:]}')
    return result


def wait_for(probe, label, timeout=60):
    deadline = time.monotonic()+timeout
    while time.monotonic() < deadline:
        try:
            if probe():
                return
        except (OSError, RuntimeError, urllib.error.URLError):
            pass
        time.sleep(0.2)
    raise RuntimeError(f'Timed out waiting for {label}')


def smoke(report):
    if platform.system() != 'Linux' or platform.machine() not in ('x86_64','amd64'):
        raise RuntimeError('The supplied monolith base is Linux amd64; run this test natively.')
    if os.environ.get('DOCKER_HOST', '').strip() not in ('', 'unix:///var/run/docker.sock'):
        raise RuntimeError('Refusing a non-local Docker host.')
    endpoint = run('docker','context','inspect','--format','{{.Endpoints.docker.Host}}').stdout.strip()
    if not endpoint.startswith('unix://'):
        raise RuntimeError('Refusing a remote Docker context.')
    run('docker','info')
    source = run('git','rev-parse','HEAD').stdout.strip()
    name = 'pinable-monolith-ci-'+secrets.token_hex(6)
    image = name+':test'
    result = {'source_commit':source, 'target':'linux/amd64', 'status':'failed', 'checks':[]}
    checks = result['checks']
    with tempfile.TemporaryDirectory(prefix='pinable-monolith-ci-', ignore_cleanup_errors=True) as temp:
        root = Path(temp)
        app, data = root/'app', root/'data'
        app.mkdir()
        data.mkdir()
        key1, key2, management = (secrets.token_hex(24) for _ in range(3))
        env = dict(os.environ, APP_PASSWORD=secrets.token_hex(24), ROOT_PASSWORD=secrets.token_hex(24))
        (data/'config.yaml').write_text(
            f'host: "0.0.0.0"\nport: 8317\nauth-dir: "/data/auths"\napi-keys:\n  - "{key1}"\n'
            f'remote-management:\n  allow-remote: true\n  secret-key: "{management}"\n'
            '  disable-control-panel: true\nplugins:\n  dir: "/data/plugins"\nlogging-to-file: true\n')
        keyfile = root/'release-key'
        run('ssh-keygen','-q','-t','ed25519','-N','','-f',str(keyfile))
        known_hosts = root/'known_hosts'
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            context = root/'context'
            shutil.copytree(ROOT/'deploy/monolith/base', context/'deploy/monolith/base')
            run('docker','build','--platform','linux/amd64','-t',image,
                '-f',str(context/'deploy/monolith/base/Dockerfile'),str(context),timeout=600)
            checks.append('base-image-build')
            run('docker','run','-d','--name',name,'--platform','linux/amd64',
                '-p','127.0.0.1::22','-p','127.0.0.1::8317',
                '-v',f'{app}:/app','-v',f'{data}:/data',
                '-e','APP_PASSWORD','-e','ROOT_PASSWORD',image,env=env)
            wait_for(lambda: run('docker','exec',name,'id','deploy',check=False).returncode == 0, 'deploy user')
            ssh_port = run('docker','port',name,'22/tcp').stdout.strip().rsplit(':',1)[1]
            http_port = run('docker','port',name,'8317/tcp').stdout.strip().rsplit(':',1)[1]
            hostkey = run('docker','exec',name,'cat','/etc/ssh/ssh_host_ed25519_key.pub').stdout.split()
            known_hosts.write_text(f'[127.0.0.1]:{ssh_port} {hostkey[0]} {hostkey[1]}\n')
            run('docker','exec','-i',name,'sh','-c',
                'install -d -m 700 -o deploy -g deploy /home/deploy/.ssh; '
                'cat > /home/deploy/.ssh/authorized_keys; '
                'chown deploy:deploy /home/deploy/.ssh/authorized_keys; '
                'chmod 600 /home/deploy/.ssh/authorized_keys', input=keyfile.with_suffix('.pub').read_text())
            ssh_options = ['-i',str(keyfile),'-o','BatchMode=yes','-o','ConnectTimeout=5',
                           '-o','StrictHostKeyChecking=yes','-o',f'UserKnownHostsFile={known_hosts}']
            wait_for(lambda: run('ssh',*ssh_options,'-p',ssh_port,'deploy@127.0.0.1','true',
                                  check=False,timeout=10).returncode == 0, 'container SSH')
            checks.append('key-authentication-with-pinned-host-key')

            def request(path, key=None, payload=None):
                headers = {'Authorization':'Bearer '+key} if key else {}
                body = None
                if payload is not None:
                    body = json.dumps(payload).encode()
                    headers['Content-Type'] = 'application/json'
                req = urllib.request.Request(f'http://127.0.0.1:{http_port}'+path,
                    data=body, headers=headers, method='PUT' if payload is not None else 'GET')
                try:
                    with opener.open(req,timeout=5) as response:
                        return response.status, json.load(response)
                except urllib.error.HTTPError as error:
                    return error.code, None

            deploy_env = dict(os.environ, REMOTE_HOST='127.0.0.1', REMOTE_PORT=ssh_port,
                REMOTE_USER='deploy', REMOTE_APP_DIR='/app', REMOTE_DATA_DIR='/data',
                SSH_OPTS=' '.join(ssh_options), CONTAINER_NAME=name)
            previous_release = None
            for version in ('ci-first','ci-second'):
                build_env = dict(os.environ, CGO_ENABLED='1', APP_NAME='cliproxyapi',
                                 TARGET_OS='linux', TARGET_ARCH='amd64')
                run('bash','scripts/monolith/build-monolith.sh','--version',version,
                    '--output-root',str(root/'packages'),env=build_env,timeout=600)
                package = root/'packages'/f'cliproxyapi-{version}-linux-amd64.tar.gz'
                with tarfile.open(package) as archive:
                    files = {str(Path(m.name).relative_to(f'cliproxyapi-{version}-linux-amd64'))
                             for m in archive.getmembers() if m.isfile()}
                    assert files == {'cli-proxy-api','VERSION','bin/start.sh','bin/stop.sh','bin/restart.sh','bin/status.sh'}
                run('bash','scripts/monolith/deploy-monolith.sh','--package',str(package),
                    '--container',name,env=deploy_env,timeout=90)
                wait_for(lambda: request('/healthz') == (200,{'status':'ok'}), 'API health')
                assert request('/v1/models')[0] == 401
                current = run('docker','exec',name,'readlink','/app/current').stdout.strip()
                assert current != previous_release and version in current
                version_text = run('docker','exec',name,'/app/current/cli-proxy-api','--version').stdout
                assert version in version_text and source[:7] in version_text
                checks.append(version+'-build-ssh-upload-restart-health-version')
                if version == 'ci-first':
                    assert request('/v1/models',key1)[0] == 200
                    assert request('/v8/management/config/access/api-keys',management,[key2])[0] == 200
                    wait_for(lambda: request('/v1/models',key2)[0] == 200, 'migrated client key')
                    assert request('/v1/models',key1)[0] == 401
                    for folder in ('auths','plugins','static','logs'):
                        run('docker','exec',name,'sh','-c',f'echo persistent-ci-marker > /data/{folder}/.monolith-ci-marker')
                    snapshot = run('docker','exec',name,'cat','/data/config.yaml').stdout
                    checks.append('authenticated-v8-config-write-and-reload')
                else:
                    assert request('/v1/models',key2)[0] == 200
                    assert request('/v1/models',key1)[0] == 401
                    assert run('docker','exec',name,'cat','/data/config.yaml').stdout == snapshot
                    assert run('docker','exec',name,'test','-d',previous_release).returncode == 0
                    for folder in ('auths','plugins','static','logs'):
                        assert run('docker','exec',name,'cat',f'/data/{folder}/.monolith-ci-marker').stdout.strip() == 'persistent-ci-marker'
                    checks.append('config-and-data-survive-second-release')
                previous_release = current
            previous_http_port = http_port
            run('docker','restart',name,timeout=90)
            # Query the active mapping again: the fixture requests an ephemeral host port.
            http_port = run('docker','port',name,'8317/tcp').stdout.strip().rsplit(':',1)[1]
            result['restart_http_ports'] = {'before':previous_http_port, 'after':http_port}
            wait_for(lambda: request('/healthz')[0] == 200, 'container restart')
            assert request('/v1/models',key2)[0] == 200
            assert run('docker','exec',name,'cat','/data/config.yaml').stdout == snapshot
            checks.append('container-restart-keeps-current-release-and-config')
            result['status'] = 'passed'
        except Exception:
            print(run('docker','logs','--tail','80',name,check=False).stdout)
            raise
        finally:
            run('docker','exec','-u','0',name,'chown','-R',f'{os.getuid()}:{os.getgid()}',
                '/app','/data',check=False)
            run('docker','rm','-f',name,check=False)
            run('docker','image','rm',image,check=False)
            report.parent.mkdir(parents=True,exist_ok=True)
            report.write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result,indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run-disposable-docker', action='store_true', required=True)
    parser.add_argument('--report',type=Path,required=True)
    args = parser.parse_args()
    smoke(args.report.resolve())
