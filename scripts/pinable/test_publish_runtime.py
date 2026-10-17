#!/usr/bin/env python3
"""Publish-policy tests use isolated fixtures; they never call GitHub."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import publish_runtime
import runtime
from test_runtime import COMMIT, executable, metadata


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.source = self.root / 'source'
        self.source.mkdir()
        license_file = self.root / 'LICENSE'
        license_file.write_text('license fixture\n')
        for target in runtime.TARGETS:
            binary = self.root / 'fixture'
            binary.write_bytes(executable(target))
            runtime.package(binary, license_file, self.source / target.replace('/', '-'), metadata(target))
        self.output = self.root / 'release'
        self.index = runtime.index(self.source, self.output, COMMIT)
        self.validation = {'source_commit': COMMIT, 'quality': 'passed', 'native_smoke': [
            {'target': m['target'], 'status': 'passed', 'source_commit': COMMIT,
             'binary_sha256': m['binary']['sha256']} for m in self.index['targets']]}
        (self.output / 'validation.json').write_bytes(runtime.encoded(self.validation))
        self.checksums()
        self.env = {'GITHUB_REPOSITORY': 'PinableAgents/CLIProxyAPI',
                    'GITHUB_EVENT_NAME': 'workflow_dispatch', 'GITHUB_REF': 'refs/heads/main'}

    def checksums(self):
        lines = [f'{runtime.digest(p.read_bytes())}  {p.name}\n' for p in sorted(self.output.iterdir())
                 if p.name != 'SHA256SUMS']
        (self.output / 'SHA256SUMS').write_text(''.join(lines))

    def test_complete_release_validation(self):
        index, files = publish_runtime.validate(self.output, COMMIT)
        self.assertEqual(index, self.index)
        self.assertEqual(len(files), 15)

    def test_extra_file_cannot_be_published(self):
        (self.output / 'accidental-secret.txt').write_text('not a release file')
        with self.assertRaisesRegex(ValueError, 'unexpected release file'):
            publish_runtime.validate(self.output, COMMIT)

    def test_incomplete_smoke_cannot_be_published(self):
        self.validation['native_smoke'].pop()
        (self.output / 'validation.json').write_bytes(runtime.encoded(self.validation))
        self.checksums()
        with self.assertRaisesRegex(ValueError, 'smoke'):
            publish_runtime.validate(self.output, COMMIT)

    def test_failing_quality_cannot_be_published(self):
        self.validation['quality'] = 'failed'
        (self.output / 'validation.json').write_bytes(runtime.encoded(self.validation))
        with self.assertRaisesRegex(ValueError, 'quality'):
            publish_runtime.validate(self.output, COMMIT)

    def test_tampered_checksums_rejected(self):
        with (self.output / 'SHA256SUMS').open('a') as out:
            out.write('0' * 64 + '  ../outside\n')
        with self.assertRaisesRegex(ValueError, 'unsafe'):
            publish_runtime.validate(self.output, COMMIT)

    def test_non_manual_or_non_main_publish_does_not_call_api(self):
        for key, value in [('GITHUB_EVENT_NAME', 'pull_request'), ('GITHUB_EVENT_NAME', 'push'),
                           ('GITHUB_REF', 'refs/heads/topic'), ('GITHUB_REPOSITORY', 'other/repo')]:
            with self.subTest(key=key, value=value), mock.patch.dict(os.environ, dict(self.env, **{key:value})), \
                 mock.patch.object(publish_runtime, 'api') as api, self.assertRaises(ValueError):
                publish_runtime.publish(self.output, COMMIT)
            api.assert_not_called()

    def test_moved_main_or_existing_tag_stops_publication(self):
        cases = [([{'object': {'sha': 'b' * 40}}], 'main moved'),
                 ([{'object': {'sha': COMMIT}}, [{'ref': 'refs/tags/pinable-runtime-test-1'}]], 'already exists')]
        for responses, message in cases:
            with mock.patch.dict(os.environ, self.env), \
                 mock.patch.object(publish_runtime, 'api', side_effect=responses) as api, \
                 mock.patch.object(publish_runtime.subprocess, 'run') as run, \
                 self.assertRaisesRegex(ValueError, message):
                publish_runtime.publish(self.output, COMMIT)
            run.assert_not_called()
            self.assertTrue(all(len(call.args) == 1 for call in api.call_args_list))

    def test_draft_then_upload_then_publish_without_clobber(self):
        with mock.patch.dict(os.environ, self.env), \
             mock.patch.object(publish_runtime, 'api', side_effect=[{'object': {'sha': COMMIT}}, [],
                 {'id': 42, 'html_url': 'https://example.invalid/release'}]) as api, \
             mock.patch.object(publish_runtime.subprocess, 'run') as run:
            publish_runtime.publish(self.output, COMMIT)
        payload = api.call_args_list[-1].args[1]
        self.assertTrue(payload['draft'])
        self.assertTrue(payload['prerelease'])
        self.assertEqual(payload['make_latest'], 'false')
        self.assertEqual(run.call_count, 2)
        self.assertNotIn('--clobber', run.call_args_list[0].args[0])
        self.assertFalse(json.loads(run.call_args_list[1].kwargs['input'])['draft'])


if __name__ == '__main__':
    unittest.main()
