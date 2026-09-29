/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import assert from 'node:assert/strict'

import { describe, test } from 'vitest'

import {
  getSafePluginAuthorUrl,
  parseLegacyAudioClips,
  parseTaskArtifactsResponse,
  resolveTaskPreviewMode,
  shouldLoadTaskArtifacts,
  TaskArtifactApiError,
} from '../lib/task-artifacts'
import type { TaskLog } from '../types'

const artifactAccessToken = `${'A'.repeat(41)}-_`

function artifactContentUrl(
  artifactKey: string,
  baseUrl = 'https://media.example.com/media-prefix'
): string {
  return `${baseUrl}/v1/tasks/task-public/artifacts/${artifactKey}/content?access=${artifactAccessToken}`
}

function taskFixture(overrides: Partial<TaskLog> = {}): TaskLog {
  return {
    id: 1,
    user_id: 7,
    platform: 'openrouter',
    task_id: 'task-public',
    action: 'generate',
    channel_id: 3,
    group: 'default',
    quota: 100,
    submit_time: 1,
    status: 'SUCCESS',
    admin_info: {
      task_plugin: {
        key: 'openrouter-video',
        name: 'OpenRouter Video',
        version: '1.0.0',
      },
    },
    ...overrides,
  }
}

describe('task artifact projection', () => {
  test('enables projection only after a successful artifact viewer opens', () => {
    const successfulPluginTask = taskFixture()

    assert.equal(shouldLoadTaskArtifacts(successfulPluginTask, false), false)
    assert.equal(shouldLoadTaskArtifacts(successfulPluginTask, true), true)
    assert.equal(
      shouldLoadTaskArtifacts(taskFixture({ status: 'IN_PROGRESS' }), true),
      false
    )
    assert.equal(
      shouldLoadTaskArtifacts(taskFixture({ admin_info: undefined }), true),
      true
    )
  })

  test('accepts an empty artifact result without inventing a preview', () => {
    assert.deepEqual(
      parseTaskArtifactsResponse({
        success: true,
        data: { artifacts: [] },
      }),
      { artifacts: [] }
    )
  })

  test('keeps stable absolute cross-origin content URLs', () => {
    assert.deepEqual(
      parseTaskArtifactsResponse({
        success: true,
        data: {
          artifacts: [
            {
              key: 'video-main',
              type: 'video',
              mime_type: 'video/mp4',
              content_url: artifactContentUrl('video-main'),
            },
            {
              key: 'poster~main',
              type: 'image',
              mime_type: 'image/webp',
              content_url: artifactContentUrl(
                'poster~main',
                'http://127.0.0.1:3001/nginx/tasks'
              ),
            },
            {
              key: 'result-file',
              type: 'file',
              content_url: artifactContentUrl(
                'result-file',
                'https://files.example.net'
              ),
            },
          ],
          legacy_content_url: artifactContentUrl(
            'video',
            'https://legacy-media.example.com/public'
          ),
        },
      }),
      {
        artifacts: [
          {
            key: 'video-main',
            type: 'video',
            mime_type: 'video/mp4',
            content_url: artifactContentUrl('video-main'),
          },
          {
            key: 'poster~main',
            type: 'image',
            mime_type: 'image/webp',
            content_url: artifactContentUrl(
              'poster~main',
              'http://127.0.0.1:3001/nginx/tasks'
            ),
          },
          {
            key: 'result-file',
            type: 'file',
            content_url: artifactContentUrl(
              'result-file',
              'https://files.example.net'
            ),
          },
        ],
        legacyContentUrl: artifactContentUrl(
          'video',
          'https://legacy-media.example.com/public'
        ),
      }
    )
  })

  test('rejects failed, malformed, or duplicate artifact results', () => {
    assert.throws(
      () =>
        parseTaskArtifactsResponse({
          success: false,
          message: 'plugin unavailable',
        }),
      TaskArtifactApiError
    )
    assert.throws(
      () =>
        parseTaskArtifactsResponse({
          success: true,
          data: {
            artifacts: [
              {
                key: 'video-main',
                type: 'video',
                content_url: artifactContentUrl('video-main'),
              },
              {
                key: 'video-main',
                type: 'image',
                content_url: artifactContentUrl('poster-main'),
              },
            ],
          },
        }),
      TaskArtifactApiError
    )
    assert.throws(
      () =>
        parseTaskArtifactsResponse({
          success: true,
          data: {
            artifacts: [
              {
                key: 'video:0',
                type: 'video',
                content_url: artifactContentUrl('video-main'),
              },
            ],
          },
        }),
      TaskArtifactApiError
    )
    assert.throws(
      () =>
        parseTaskArtifactsResponse({
          success: true,
          data: {
            artifacts: [
              {
                key: ' video-main',
                type: 'video',
                content_url: artifactContentUrl('video-main'),
              },
            ],
          },
        }),
      TaskArtifactApiError
    )
  })

  test('rejects unsafe or missing content URLs', () => {
    const validContentUrl = artifactContentUrl('video-main')
    const unsafeUrls: unknown[] = [
      undefined,
      'javascript:alert(1)',
      'data:text/plain,artifact',
      'https:media.example.com/task',
      '//media.example.com/task',
      `/v1/tasks/task-public/artifacts/video-main/content?access=${artifactAccessToken}`,
      '/\\media.example.com/task',
      validContentUrl.replace('https://', 'https://user:secret@'),
      validContentUrl.replace('https://', 'https://@'),
      `${validContentUrl}#fragment`,
      `${validContentUrl}#`,
      ` ${validContentUrl}`,
      `${validContentUrl}\n`,
      'https://media.example.com/video.mp4',
      `https://media.example.com/v1/videos/task-public/content?access=${artifactAccessToken}`,
      `https://media.example.com/v1/tasks/task-public/artifacts/video-main/content?token=${artifactAccessToken}`,
      `https://media.example.com/v1/tasks/task-public/artifacts/video-main/content?access=${'A'.repeat(42)}`,
      `${validContentUrl}&access=${artifactAccessToken}`,
      `${validContentUrl}&download=1`,
    ]

    for (const contentUrl of unsafeUrls) {
      assert.throws(
        () =>
          parseTaskArtifactsResponse({
            success: true,
            data: {
              artifacts: [
                {
                  key: 'video-main',
                  type: 'video',
                  content_url: contentUrl,
                },
              ],
            },
          }),
        TaskArtifactApiError
      )
    }

    assert.throws(
      () =>
        parseTaskArtifactsResponse({
          success: true,
          data: {
            artifacts: [],
            legacy_content_url: `https://media.example.com/v1/videos/task-public/content?access=${artifactAccessToken}`,
          },
        }),
      TaskArtifactApiError
    )
  })
})

describe('legacy task preview compatibility', () => {
  test('preserves old Suno and video previews without duplicating plugin previews', () => {
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          platform: 'suno',
          admin_info: undefined,
          data: [{ audio_url: 'https://media.example/audio.mp3' }],
        })
      ),
      'legacy-suno'
    )
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          admin_info: undefined,
          legacy_video_available: true,
        })
      ),
      'legacy-video'
    )
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          admin_info: undefined,
          legacy_video_available: true,
        }),
        true
      ),
      'plugin'
    )
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          legacy_video_available: true,
        })
      ),
      'plugin'
    )
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          status: 'FAILURE',
          legacy_video_available: true,
        })
      ),
      'none'
    )
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({
          admin_info: undefined,
          legacy_video_available: false,
        })
      ),
      'plugin'
    )
  })
})

describe('synchronous results the gateway did not retain', () => {
  test('given result_discarded on a successful task, the viewer explains instead of loading artifacts', () => {
    const discarded = taskFixture({ result_discarded: true })

    assert.equal(resolveTaskPreviewMode(discarded), 'discarded')
    assert.equal(resolveTaskPreviewMode(discarded, true), 'discarded')
    assert.equal(shouldLoadTaskArtifacts(discarded, true), false)
  })

  test('given result_discarded on a failed task, the failed state still wins', () => {
    assert.equal(
      resolveTaskPreviewMode(
        taskFixture({ status: 'FAILURE', result_discarded: true })
      ),
      'none'
    )
  })

  test('given a retained result, plugin previews load as before', () => {
    const retained = taskFixture({ result_discarded: false })

    assert.equal(resolveTaskPreviewMode(retained), 'plugin')
    assert.equal(shouldLoadTaskArtifacts(retained, true), true)
  })
})

describe('plugin author links', () => {
  test('allows HTTP authors and rejects executable URL schemes', () => {
    assert.equal(
      getSafePluginAuthorUrl({
        name: 'Community Maintainer',
        url: 'https://plugins.example.com/maintainer',
      }),
      'https://plugins.example.com/maintainer'
    )
    assert.equal(
      getSafePluginAuthorUrl({
        name: 'Unsafe Maintainer',
        url: 'javascript:alert(1)',
      }),
      undefined
    )
  })
})

describe('legacy Suno audio clips', () => {
  test('keeps only playable clips from array or JSON string snapshots', () => {
    const clips = [
      { id: 'a', title: 'Song A', audio_url: 'https://media.example/a.mp3' },
      { id: 'b', title: 'No audio' },
      'not a clip',
    ]
    const expected = [
      { id: 'a', title: 'Song A', audio_url: 'https://media.example/a.mp3' },
    ]
    assert.deepEqual(parseLegacyAudioClips(clips), expected)
    assert.deepEqual(parseLegacyAudioClips(JSON.stringify(clips)), expected)
    assert.deepEqual(parseLegacyAudioClips('{not json'), [])
    assert.deepEqual(parseLegacyAudioClips(undefined), [])
  })

  test('projects legacy_audio_clips from the artifacts response', () => {
    assert.deepEqual(
      parseTaskArtifactsResponse({
        success: true,
        data: {
          artifacts: [],
          legacy_audio_clips: [
            { audio_url: 'https://media.example/a.mp3', title: 'Song A' },
          ],
        },
      }),
      {
        artifacts: [],
        legacyAudioClips: [
          { audio_url: 'https://media.example/a.mp3', title: 'Song A' },
        ],
      }
    )
    assert.deepEqual(
      parseTaskArtifactsResponse({ success: true, data: { artifacts: [] } }),
      { artifacts: [] }
    )
  })
})
