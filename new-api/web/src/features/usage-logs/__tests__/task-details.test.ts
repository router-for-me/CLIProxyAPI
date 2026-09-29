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

import { resolveTaskDetailAccess } from '../lib/task-details'
import type { TaskLog } from '../types'

const task: TaskLog = {
  id: 1,
  user_id: 7,
  platform: 'document-parser',
  task_id: 'task_public',
  action: 'GENERATE',
  channel_id: 3,
  group: 'default',
  quota: 100,
  submit_time: 1,
  status: 'SUCCESS',
  admin_info: {
    task_plugin: {
      key: 'document-parser',
      name: 'Document Parser',
      version: '1.2.3',
      author: {
        name: 'Community Maintainer',
        url: 'https://plugins.example.com/maintainers/community',
      },
    },
  },
  root_info: {
    task_plugin: {
      key: 'document-parser',
      version: '1.2.3',
      api_version: 1,
      generation: 42,
    },
    upstream_task_id: 'upstream-private',
    node_name: 'node-a',
  },
}

describe('task detail access', () => {
  test('does not expose elevated fields in a self view', () => {
    assert.deepEqual(resolveTaskDetailAccess(task, false, false), {})
  })

  test('gives admins plugin identity without root diagnostics', () => {
    assert.deepEqual(resolveTaskDetailAccess(task, true, false), {
      plugin: task.admin_info?.task_plugin,
    })
  })

  test('adds runtime and upstream diagnostics for root', () => {
    assert.deepEqual(resolveTaskDetailAccess(task, true, true), {
      plugin: task.admin_info?.task_plugin,
      runtime: task.root_info?.task_plugin,
      upstreamTaskId: 'upstream-private',
      nodeName: 'node-a',
    })
  })
})
