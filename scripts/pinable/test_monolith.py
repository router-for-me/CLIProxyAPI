#!/usr/bin/env python3
"""Offline monolith-script regressions; SSH, SCP and Go are isolated fixtures."""
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ('build-monolith.sh', 'build-monolith-base-remote.sh',
           'start-monolith-remote.sh', 'deploy-monolith.sh')
FAKE = r'''import json, os, sys
from pathlib import Path
name = Path(sys.argv[0]).name
args = sys.argv[1:]
body = sys.stdin.read() if name == 'ssh' and args[-1:] == ['bash -s'] else ''
with open(os.environ['CALLS'], 'a') as f:
    f.write(json.dumps({'name':name, 'args':args, 'body':body,
                       'cgo':os.environ.get('CGO_ENABLED'),
                       'os':os.environ.get('GOOS'), 'arch':os.environ.get('GOARCH')})+'\n')
if name == 'go':
    if os.environ.get('FAIL_GO') == '1': sys.exit(17)
    if args[0] == 'build':
        p = Path(args[args.index('-o')+1])
        p.write_text('#!/bin/sh\nprintf "fixture-binary\\n"\n')
        p.chmod(0o755)
if name == 'ssh' and os.environ.get('FAIL_SSH_MATCH'):
    if os.environ['FAIL_SSH_MATCH'] in (' '.join(args)+'\n'+body): sys.exit(19)
if name == 'scp' and os.environ.get('FAIL_SCP') == '1': sys.exit(23)
'''


class MonolithTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='pinable-monolith-test-')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / 'repo'
        for folder in ('scripts/monolith', 'deploy/monolith/base'):
            shutil.copytree(ROOT / folder, self.repo / folder)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        for name in ('go', 'ssh', 'scp', 'docker'):
            p = self.bin / name
            p.write_text('#!' + sys.executable + '\n' + FAKE)
            p.chmod(0o755)
        self.calls_file = self.root / 'calls.jsonl'
        self.env = {k:v for k,v in os.environ.items() if not k.startswith(
            ('REMOTE_', 'APP_', 'ROOT_PASSWORD', 'TARGET_', 'SSH_OPTS', 'VERSION',
             'OUTPUT_ROOT', 'HTTP_PORT', 'SSH_PORT', 'OAUTH_CALLBACK_PORTS',
             'CONTAINER_NAME', 'IMAGE_NAME', 'SKIP_GO_MOD_DOWNLOAD', 'CGO_ENABLED'))}
        self.env.update(PATH=str(self.bin)+os.pathsep+os.environ['PATH'],
                        CALLS=str(self.calls_file), VERSION='fixture',
                        GIT_CONFIG_GLOBAL=os.devnull, GIT_CONFIG_NOSYSTEM='1')

    def run_script(self, name, *args, success=True, **env):
        result = subprocess.run(['bash', str(self.repo / 'scripts/monolith' / name), *args],
            cwd=self.root, env=dict(self.env, **env), text=True,
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=20)
        if success:
            self.assertEqual(result.returncode, 0, result.stdout)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        return result

    def calls(self, name=None):
        rows = [json.loads(s) for s in self.calls_file.read_text().splitlines()] if self.calls_file.exists() else []
        return [r for r in rows if name is None or r['name'] == name]

    def build(self, **env):
        output = self.root / 'release packages'
        self.run_script('build-monolith.sh', '--version', 'test-release',
                        '--output-root', str(output), **env)
        return output / 'cliproxyapi-test-release-linux-amd64'

    def test_scripts_parse_and_keep_executable_modes(self):
        paths = list((ROOT/'scripts/monolith').glob('*.sh')) + [ROOT/'deploy/monolith/base/entrypoint.sh']
        self.assertEqual(len(paths), 5)
        for path in paths:
            self.assertEqual(subprocess.run(['bash', '-n', str(path)]).returncode, 0)
            self.assertTrue(path.stat().st_mode & 0o111, str(path))

    def test_help_is_side_effect_free(self):
        for name in SCRIPTS:
            self.assertIn('Usage:', self.run_script(name, '--help').stdout)
        self.assertEqual(self.calls(), [])

    def test_bad_arguments_do_not_contact_hosts(self):
        for name in SCRIPTS:
            self.run_script(name, '--unknown', success=False)
        for name in ('build-monolith-base-remote.sh', 'start-monolith-remote.sh'):
            self.run_script(name, success=False)
            self.run_script(name, '--host', success=False)
        self.run_script('deploy-monolith.sh', success=False)
        self.run_script('deploy-monolith.sh', '--package', '/nonexistent/fixture.tar.gz', success=False)
        self.assertEqual(self.calls(), [])

    def test_package_has_only_binary_helpers_and_metadata(self):
        package = self.build()
        with tarfile.open(str(package)+'.tar.gz') as archive:
            entries = {str(Path(m.name).relative_to(package.name)):m for m in archive.getmembers() if m.isfile()}
            self.assertEqual(set(entries), {'cli-proxy-api', 'VERSION', 'bin/start.sh',
                                           'bin/stop.sh', 'bin/restart.sh', 'bin/status.sh'})
            self.assertTrue(all(m.mode & 0o111 for n,m in entries.items() if n != 'VERSION'))
            self.assertTrue(all(not m.issym() and not m.islnk() for m in archive.getmembers()))
        self.assertIn('VERSION=test-release\n', (package/'VERSION').read_text())
        self.assertIn('TARGET=linux/amd64\n', (package/'VERSION').read_text())
        for path in (package/'bin').glob('*.sh'):
            self.assertEqual(subprocess.run(['bash','-n',str(path)]).returncode, 0)

    def test_build_profile_and_download_override(self):
        self.build(SKIP_GO_MOD_DOWNLOAD='1', CGO_ENABLED='0')
        rows = self.calls('go')
        self.assertEqual(len(rows), 1)
        self.assertEqual((rows[0]['cgo'], rows[0]['os'], rows[0]['arch']), ('0','linux','amd64'))
        self.assertIn('-buildvcs=false', rows[0]['args'])
        self.assertIn('main.Version=test-release', rows[0]['args'][rows[0]['args'].index('-ldflags')+1])

    def test_default_build_downloads_modules_and_enables_cgo(self):
        self.build()
        rows = self.calls('go')
        self.assertEqual(rows[0]['args'], ['mod','download'])
        self.assertEqual(rows[1]['cgo'], '1')

    def test_build_failure_does_not_create_tarball(self):
        output = self.root/'failed'
        self.run_script('build-monolith.sh', '--output-root', str(output), success=False, FAIL_GO='1')
        self.assertEqual(list(output.glob('*.tar.gz')), [])

    def test_generated_foreground_requires_persistent_config(self):
        package = self.build()
        data = self.root/'data'
        result = subprocess.run(['bash',str(package/'bin/start.sh'),'foreground'],
            env=dict(self.env, DATA_DIR=str(data)), capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('config file not found', result.stderr)
        (data/'config.yaml').write_text('port: 8317\n')
        result = subprocess.run(['bash',str(package/'bin/start.sh'),'foreground'],
            env=dict(self.env, DATA_DIR=str(data)), capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, 'fixture-binary\n')

    def test_remote_base_uploads_only_build_context(self):
        self.run_script('build-monolith-base-remote.sh', '--host', 'example.invalid', '--rebuild')
        uploaded = self.calls('scp')
        self.assertEqual(len(uploaded), 1)
        self.assertIn('deploy/monolith/base/Dockerfile', uploaded[0]['args'])
        body = self.calls('ssh')[-1]['body']
        self.assertIn('docker build --platform linux/amd64', body)
        self.assertIn("if [ '1' -eq 1 ]", body)
        self.assertNotIn('docker run', body)

    def test_remote_start_preserves_mounts_ports_and_explicit_recreate(self):
        self.run_script('start-monolith-remote.sh', '--host', 'example.invalid', '--recreate',
            HTTP_PORT='127.0.0.1:18082', SSH_PORT='127.0.0.1:18524',
            OAUTH_CALLBACK_PORTS='1455 54545', APP_PASSWORD='test-only', ROOT_PASSWORD='test-only')
        body = self.calls('ssh')[-1]['body']
        self.assertIn("'127.0.0.1:18082:8317'", body)
        self.assertIn("'127.0.0.1:18524:22'", body)
        self.assertIn("'/data/monolith-cliproxyapi:/data'", body)
        self.assertIn('-p 1455:1455 -p 54545:54545', body)
        self.assertIn("if [ '1' -eq 1 ]", body)

    def test_deploy_orders_upload_switch_restart_and_status(self):
        package = self.build()
        self.calls_file.unlink()
        result = self.run_script('deploy-monolith.sh', '--package', str(package)+'.tar.gz',
                                '--host', 'example.invalid')
        rows = self.calls()
        self.assertEqual([r['name'] for r in rows], ['ssh','scp','ssh','ssh','ssh'])
        self.assertIn('--strip-components=1', rows[2]['body'])
        self.assertIn('./bin/restart.sh', rows[3]['args'][-1])
        self.assertIn('./bin/status.sh', rows[4]['args'][-1])
        self.assertIn('Deploy complete', result.stdout)
        self.assertNotIn('/data/config.yaml', rows[2]['body'])

    def test_deploy_failures_are_not_reported_as_success(self):
        package = self.build()
        for phase in ('--strip-components=1', './bin/restart.sh', '&& ./bin/status.sh'):
            with self.subTest(phase=phase):
                result = self.run_script('deploy-monolith.sh', '--package', str(package)+'.tar.gz',
                    '--host', 'example.invalid', success=False, FAIL_SSH_MATCH=phase)
                self.assertNotIn('Deploy complete', result.stdout)
                self.assertIn('Collecting diagnostics', result.stdout)
        result = self.run_script('deploy-monolith.sh', '--package', str(package)+'.tar.gz',
            '--host', 'example.invalid', success=False, FAIL_SCP='1')
        self.assertNotIn('Deploy complete', result.stdout)


if __name__ == '__main__':
    unittest.main()
