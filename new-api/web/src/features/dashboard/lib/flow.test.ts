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
import { describe, expect, test } from 'vitest'

import type { FlowQuotaDataItem } from '../types'
import {
  buildDashboardFlowData,
  buildFlowFilterOptions,
  buildFlowSankeySpec,
} from './flow'

const rows: FlowQuotaDataItem[] = [
  {
    user_id: 1,
    username: 'alice',
    node_name: 'node-a',
    token_id: 11,
    token_name: 'primary',
    use_group: 'vip',
    channel_id: 101,
    channel_name: 'east',
    model_name: 'gpt-4.1',
    quota: 100,
    token_used: 40,
    count: 2,
  },
  {
    user_id: 1,
    username: 'alice',
    node_name: 'node-a',
    token_id: 11,
    token_name: 'primary',
    use_group: 'vip',
    channel_id: 102,
    channel_name: 'west',
    model_name: 'gpt-4.1',
    quota: 50,
    token_used: 20,
    count: 1,
  },
  {
    user_id: 2,
    username: 'bob',
    node_name: 'node-b',
    token_id: 22,
    token_name: 'backup',
    use_group: 'default',
    channel_id: 101,
    channel_name: 'east',
    model_name: 'claude-4-sonnet',
    quota: 70,
    token_used: 30,
    count: 3,
  },
]

const topLimitRows: FlowQuotaDataItem[] = [
  {
    user_id: 1,
    username: 'alpha',
    use_group: 'vip',
    channel_id: 201,
    channel_name: 'channel-a',
    model_name: 'model-a',
    quota: 100,
    token_used: 1_000,
    count: 1,
  },
  {
    user_id: 2,
    username: 'beta',
    use_group: 'default',
    channel_id: 202,
    channel_name: 'channel-b',
    model_name: 'model-b',
    quota: 80,
    token_used: 10,
    count: 20,
  },
  {
    user_id: 3,
    username: 'gamma',
    use_group: 'free',
    channel_id: 203,
    channel_name: 'channel-c',
    model_name: 'model-c',
    quota: 10,
    token_used: 2_000,
    count: 5,
  },
]

describe('dashboard flow data', () => {
  test('builds normal user token-group-model flow', () => {
    const result = buildDashboardFlowData(rows.slice(0, 2), 'quota', {
      role: 'user',
    })

    expect(result.summary.quota).toBe(150)
    expect(result.summary.tokens).toBe(60)
    expect(result.summary.requests).toBe(3)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:vip', 'model:gpt-4.1', 150],
      ['token:11', 'group:vip', 150],
    ])
    expect(result.flow.nodes.some((node) => node.kind === 'channel')).toBe(
      false
    )
  })

  test('builds admin user-group-model-channel flow', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
    })

    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:default', 'model:claude-4-sonnet', 70],
      ['group:vip', 'model:gpt-4.1', 150],
      ['model:claude-4-sonnet', 'channel:101', 70],
      ['model:gpt-4.1', 'channel:101', 100],
      ['model:gpt-4.1', 'channel:102', 50],
      ['user:1', 'group:vip', 150],
      ['user:2', 'group:default', 70],
    ])
  })

  test('builds root user-node-token-group-model-channel flow', () => {
    const result = buildDashboardFlowData(rows, 'requests', {
      role: 'root',
    })

    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:default', 'model:claude-4-sonnet', 3],
      ['group:vip', 'model:gpt-4.1', 3],
      ['model:claude-4-sonnet', 'channel:101', 3],
      ['model:gpt-4.1', 'channel:101', 2],
      ['model:gpt-4.1', 'channel:102', 1],
      ['node:node-a', 'token:11', 3],
      ['node:node-b', 'token:22', 3],
      ['token:11', 'group:vip', 3],
      ['token:22', 'group:default', 3],
      ['user:1', 'node:node-a', 3],
      ['user:2', 'node:node-b', 3],
    ])
  })

  test('filters by selected users', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedUsers: ['user:2'],
    })

    expect(result.summary.quota).toBe(70)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:default', 'model:claude-4-sonnet', 70],
      ['model:claude-4-sonnet', 'channel:101', 70],
      ['user:2', 'group:default', 70],
    ])
  })

  test('filters rows by selected flow nodes', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedNodes: [{ kind: 'model', id: 'model:gpt-4.1' }],
    })

    expect(result.summary.quota).toBe(150)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:vip', 'model:gpt-4.1', 150],
      ['model:gpt-4.1', 'channel:101', 100],
      ['model:gpt-4.1', 'channel:102', 50],
      ['user:1', 'group:vip', 150],
    ])
  })

  test('combines node filters with OR inside a column and AND across columns', () => {
    const sameColumn = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedNodes: [
        { kind: 'model', id: 'model:gpt-4.1' },
        { kind: 'model', id: 'model:claude-4-sonnet' },
      ],
    })
    const crossColumn = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedNodes: [
        { kind: 'model', id: 'model:gpt-4.1' },
        { kind: 'channel', id: 'channel:101' },
      ],
    })

    expect(sameColumn.summary.quota).toBe(220)
    expect(crossColumn.summary.quota).toBe(100)
    expect(
      crossColumn.flow.links.map((link) => [
        link.source,
        link.target,
        link.value,
      ])
    ).toEqual([
      ['group:vip', 'model:gpt-4.1', 100],
      ['model:gpt-4.1', 'channel:101', 100],
      ['user:1', 'group:vip', 100],
    ])
  })

  test('combines user and node filters', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedUsers: ['user:1'],
      selectedNodes: [{ kind: 'channel', id: 'channel:101' }],
    })

    expect(result.summary.quota).toBe(100)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:vip', 'model:gpt-4.1', 100],
      ['model:gpt-4.1', 'channel:101', 100],
      ['user:1', 'group:vip', 100],
    ])
  })

  test('reconnects links when a middle stage is hidden', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      visibleStages: ['user', 'model', 'channel'],
    })

    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['model:claude-4-sonnet', 'channel:101', 70],
      ['model:gpt-4.1', 'channel:101', 100],
      ['model:gpt-4.1', 'channel:102', 50],
      ['user:1', 'model:gpt-4.1', 150],
      ['user:2', 'model:claude-4-sonnet', 70],
    ])
    expect(result.flow.nodes.some((node) => node.kind === 'group')).toBe(false)
  })

  test('ignores stage filters that would leave fewer than two columns', () => {
    const result = buildDashboardFlowData(rows.slice(0, 2), 'quota', {
      role: 'user',
      visibleStages: ['model'],
    })

    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:vip', 'model:gpt-4.1', 150],
      ['token:11', 'group:vip', 150],
    ])
  })

  test('builds user filter options with stable values', () => {
    const options = buildFlowFilterOptions(rows, 'quota')

    expect(
      options.users.map((user) => [user.value, user.label, user.valueLabel])
    ).toEqual([
      ['user:1', 'alice', '150'],
      ['user:2', 'bob', '70'],
    ])
    expect(options.users[0].color).not.toBe(options.users[1].color)
  })

  test('builds node filter options without applying top limits', () => {
    const result = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })

    expect(
      result.filterOptions.nodes.some(
        (option) => option.kind === 'model' && option.value === 'model:model-c'
      )
    ).toBe(true)
    expect(
      result.filterOptions.nodes
        .filter((option) => option.kind === 'model')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['model:model-a', '100'],
      ['model:model-b', '80'],
      ['model:model-c', '10'],
    ])
  })

  test('facets node filter options by selected nodes from other columns', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      selectedNodes: [{ kind: 'node', id: 'node:node-a' }],
    })
    const nodeOptions = result.filterOptions.nodes

    expect(
      nodeOptions
        .filter((option) => option.kind === 'node')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['node:node-a', '150'],
      ['node:node-b', '70'],
    ])
    expect(
      nodeOptions
        .filter((option) => option.kind === 'token')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([['token:11', '150']])
    expect(
      nodeOptions
        .filter((option) => option.kind === 'channel')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['channel:101', '100'],
      ['channel:102', '50'],
    ])
  })

  test('keeps same-column node options available for OR filtering', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      selectedNodes: [{ kind: 'model', id: 'model:gpt-4.1' }],
    })

    expect(
      result.filterOptions.nodes
        .filter((option) => option.kind === 'model')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['model:gpt-4.1', '150'],
      ['model:claude-4-sonnet', '70'],
    ])
    expect(
      result.filterOptions.nodes
        .filter((option) => option.kind === 'channel')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['channel:101', '100'],
      ['channel:102', '50'],
    ])
  })

  test('combines user filters with faceted node filter options', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      selectedUsers: ['user:1'],
      selectedNodes: [{ kind: 'channel', id: 'channel:101' }],
    })

    expect(result.summary.quota).toBe(100)
    expect(
      result.filterOptions.nodes
        .filter((option) => option.kind === 'model')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([['model:gpt-4.1', '100']])
    expect(
      result.filterOptions.nodes
        .filter((option) => option.kind === 'channel')
        .map((option) => [option.value, option.valueLabel])
    ).toEqual([
      ['channel:101', '100'],
      ['channel:102', '50'],
    ])
  })

  test('aggregates overflow nodes into per-column Other buckets', () => {
    const result = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      topNodeLimit: 2,
      overflowMode: 'aggregate',
      otherNodeLabel: (kind) => `Other ${kind}`,
    })
    const nodeIds = new Set(result.flow.nodes.map((node) => node.id))
    const otherUser = result.flow.nodes.find(
      (node) => node.id === 'user:__other__'
    )
    const otherFirstStepLink = result.flow.links.find(
      (link) =>
        link.source === 'user:__other__' && link.target === 'group:__other__'
    )
    const firstStepTotal = result.flow.links
      .filter((link) => link.source.startsWith('user:'))
      .reduce((sum, link) => sum + link.value, 0)

    expect(result.summary.quota).toBe(190)
    expect(firstStepTotal).toBe(190)
    expect(otherUser?.label).toBe('Other user')
    expect(otherFirstStepLink?.value).toBe(10)
    expect(nodeIds.has('user:3')).toBe(false)
    expect(nodeIds.has('group:free')).toBe(false)
    expect(nodeIds.has('model:model-c')).toBe(false)
    expect(nodeIds.has('channel:203')).toBe(false)
    expect(nodeIds.has('user:__other__')).toBe(true)
    expect(nodeIds.has('group:__other__')).toBe(true)
    expect(nodeIds.has('model:__other__')).toBe(true)
    expect(nodeIds.has('channel:__other__')).toBe(true)
  })

  test('hides overflow paths when overflow mode is hide', () => {
    const result = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      topNodeLimit: 2,
      overflowMode: 'hide',
      otherNodeLabel: (kind) => `Other ${kind}`,
    })
    const nodeIds = new Set(result.flow.nodes.map((node) => node.id))
    const firstStepTotal = result.flow.links
      .filter((link) => link.source.startsWith('user:'))
      .reduce((sum, link) => sum + link.value, 0)

    expect(result.summary.quota).toBe(190)
    expect(firstStepTotal).toBe(180)
    expect(nodeIds.has('user:3')).toBe(false)
    expect(nodeIds.has('user:__other__')).toBe(false)
    expect(nodeIds.has('model:__other__')).toBe(false)
  })

  test('ranks top nodes using the selected flow metric', () => {
    const byQuota = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })
    const byRequests = buildDashboardFlowData(topLimitRows, 'requests', {
      role: 'admin',
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })
    const byTokens = buildDashboardFlowData(topLimitRows, 'tokens', {
      role: 'admin',
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })

    expect(byQuota.flow.nodes.some((node) => node.id === 'user:1')).toBe(true)
    expect(byRequests.flow.nodes.some((node) => node.id === 'user:2')).toBe(
      true
    )
    expect(byTokens.flow.nodes.some((node) => node.id === 'user:3')).toBe(true)
  })

  test('applies top limits only to visible stages', () => {
    const result = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      visibleStages: ['user', 'model'],
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })
    const nodeIds = new Set(result.flow.nodes.map((node) => node.id))

    expect(nodeIds.has('user:1')).toBe(true)
    expect(nodeIds.has('user:__other__')).toBe(true)
    expect(nodeIds.has('model:model-a')).toBe(true)
    expect(nodeIds.has('model:__other__')).toBe(true)
    expect(nodeIds.has('group:__other__')).toBe(false)
    expect(nodeIds.has('channel:__other__')).toBe(false)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['user:__other__', 'model:__other__', 90],
      ['user:1', 'model:model-a', 100],
    ])
  })

  test('applies top limits after node filters', () => {
    const result = buildDashboardFlowData(topLimitRows, 'quota', {
      role: 'admin',
      selectedNodes: [{ kind: 'model', id: 'model:model-c' }],
      topNodeLimit: 1,
      overflowMode: 'aggregate',
    })
    const nodeIds = new Set(result.flow.nodes.map((node) => node.id))

    expect(result.summary.quota).toBe(10)
    expect(nodeIds.has('model:model-c')).toBe(true)
    expect(nodeIds.has('model:__other__')).toBe(false)
    expect(
      result.flow.links.map((link) => [link.source, link.target, link.value])
    ).toEqual([
      ['group:free', 'model:model-c', 10],
      ['model:model-c', 'channel:203', 10],
      ['user:3', 'group:free', 10],
    ])
  })

  test('ignores selected node filters for hidden stages', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'admin',
      visibleStages: ['user', 'model', 'channel'],
      selectedNodes: [{ kind: 'group', id: 'group:vip' }],
    })

    expect(result.summary.quota).toBe(220)
    expect(result.flow.nodes.some((node) => node.id === 'group:vip')).toBe(
      false
    )
  })

  test('highlights full paths that contain the active user node', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      activeNode: { kind: 'user', id: 'user:1' },
    })
    const nodeState = new Map(
      result.flow.nodes.map((node) => [
        node.id,
        { highlighted: node.highlighted, dimmed: node.dimmed },
      ])
    )
    const linkState = new Map(
      result.flow.links.map((link) => [
        `${link.source}->${link.target}`,
        { highlighted: link.highlighted, dimmed: link.dimmed },
      ])
    )

    expect(nodeState.get('user:1')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('node:node-a')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('model:gpt-4.1')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('channel:101')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('user:2')).toEqual({
      highlighted: false,
      dimmed: true,
    })
    expect(linkState.get('user:1->node:node-a')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(linkState.get('model:gpt-4.1->channel:101')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(linkState.get('model:claude-4-sonnet->channel:101')).toEqual({
      highlighted: false,
      dimmed: true,
    })
  })

  test('highlights full paths that traverse the active link', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      activeLink: { source: 'model:gpt-4.1', target: 'channel:101' },
    })
    const nodeState = new Map(
      result.flow.nodes.map((node) => [
        node.id,
        { highlighted: node.highlighted, dimmed: node.dimmed },
      ])
    )
    const linkState = new Map(
      result.flow.links.map((link) => [
        `${link.source}->${link.target}`,
        { highlighted: link.highlighted, dimmed: link.dimmed },
      ])
    )

    expect(linkState.get('model:gpt-4.1->channel:101')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(linkState.get('model:gpt-4.1->channel:102')).toEqual({
      highlighted: false,
      dimmed: true,
    })
    expect(nodeState.get('user:1')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('node:node-a')).toEqual({
      highlighted: true,
      dimmed: false,
    })
    expect(nodeState.get('user:2')).toEqual({
      highlighted: false,
      dimmed: true,
    })
  })

  test('highlights shared aggregate edges when they contain an active path', () => {
    const sharedRows: FlowQuotaDataItem[] = [
      {
        user_id: 1,
        username: 'alice',
        use_group: 'vip',
        channel_id: 101,
        channel_name: 'east',
        model_name: 'gpt-4.1',
        quota: 100,
        token_used: 40,
        count: 2,
      },
      {
        user_id: 2,
        username: 'bob',
        use_group: 'vip',
        channel_id: 101,
        channel_name: 'east',
        model_name: 'gpt-4.1',
        quota: 50,
        token_used: 20,
        count: 1,
      },
    ]
    const result = buildDashboardFlowData(sharedRows, 'quota', {
      role: 'admin',
      activeNode: { kind: 'user', id: 'user:1' },
    })
    const sharedLink = result.flow.links.find(
      (link) => link.source === 'group:vip' && link.target === 'model:gpt-4.1'
    )
    const inactiveUserLink = result.flow.links.find(
      (link) => link.source === 'user:2' && link.target === 'group:vip'
    )

    expect(sharedLink?.value).toBe(150)
    expect(sharedLink?.highlighted).toBe(true)
    expect(sharedLink?.dimmed).toBe(false)
    expect(inactiveUserLink?.highlighted).toBe(false)
    expect(inactiveUserLink?.dimmed).toBe(true)
  })

  test('does not emit highlight states without a visible active node', () => {
    const withoutActive = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
    })
    const hiddenActive = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      visibleStages: ['node', 'token'],
      activeNode: { kind: 'user', id: 'user:1' },
    })

    expect(
      withoutActive.flow.nodes.every(
        (node) => node.highlighted === undefined && node.dimmed === undefined
      )
    ).toBe(true)
    expect(
      withoutActive.flow.links.every(
        (link) => link.highlighted === undefined && link.dimmed === undefined
      )
    ).toBe(true)
    expect(
      hiddenActive.flow.nodes.every(
        (node) => node.highlighted === undefined && node.dimmed === undefined
      )
    ).toBe(true)
    expect(
      hiddenActive.flow.links.every(
        (link) => link.highlighted === undefined && link.dimmed === undefined
      )
    ).toBe(true)
  })

  test('builds Sankey spec with quota token request tooltips', () => {
    const result = buildDashboardFlowData(rows.slice(0, 1), 'quota', {
      role: 'root',
    })
    const flowSpec = buildFlowSankeySpec(result.flow, 'Flow')
    const values = flowSpec.data[0].values[0]
    const aliceNode = values.nodes.find(
      (node: Record<string, unknown>) => node.key === 'user:1'
    )
    const userNodeLink = values.links.find(
      (link: Record<string, unknown>) =>
        link.source === 'user:1' && link.target === 'node:node-a'
    )

    expect(flowSpec.type).toBe('sankey')
    expect(flowSpec.title.text).toBe('Flow')
    expect(flowSpec.emphasis).toEqual({ enable: false })
    expect(flowSpec.tooltip.mark.visible({ datum: aliceNode })).toBe(true)
    expect(flowSpec.tooltip.mark.visible({ datum: userNodeLink })).toBe(true)
    expect(flowSpec.animation).toBe(false)
    expect(values.nodes.length).toBe(6)
    expect(values.links.length).toBe(5)
    expect(aliceNode.name).toBe('alice')
    expect(userNodeLink.linkColor).toMatch(/^rgba\(/)

    const tooltipRows = flowSpec.tooltip.mark.content
    expect(
      tooltipRows
        .filter((row: Record<string, unknown>) =>
          typeof row.visible === 'function'
            ? row.visible({ datum: userNodeLink })
            : true
        )
        .map((row: Record<string, unknown>) => [
          row.key,
          typeof row.value === 'function'
            ? row.value({ datum: userNodeLink })
            : row.value,
        ])
    ).toEqual([
      ['Quota', '100'],
      ['Tokens', '40'],
      ['Requests', '2'],
      ['Share', '100.0%'],
    ])
  })

  test('maps active flow highlight states into the Sankey spec', () => {
    const result = buildDashboardFlowData(rows, 'quota', {
      role: 'root',
      activeNode: { kind: 'user', id: 'user:1' },
    })
    const flowSpec = buildFlowSankeySpec(result.flow, 'Flow')
    const values = flowSpec.data[0].values[0]
    const aliceNode = values.nodes.find(
      (node: Record<string, unknown>) => node.key === 'user:1'
    )
    const bobNode = values.nodes.find(
      (node: Record<string, unknown>) => node.key === 'user:2'
    )
    const highlightedLink = values.links.find(
      (link: Record<string, unknown>) =>
        link.source === 'model:gpt-4.1' && link.target === 'channel:101'
    )
    const dimmedLink = values.links.find(
      (link: Record<string, unknown>) =>
        link.source === 'model:claude-4-sonnet' && link.target === 'channel:101'
    )
    const nodeOpacity = flowSpec.node.style.fillOpacity
    const linkOpacity = flowSpec.link.style.fillOpacity

    expect(flowSpec.emphasis).toEqual({ enable: false })
    expect(aliceNode.highlighted).toBe(true)
    expect(bobNode.dimmed).toBe(true)
    expect(highlightedLink.highlighted).toBe(true)
    expect(dimmedLink.dimmed).toBe(true)
    expect(nodeOpacity(aliceNode)).toBe(1)
    expect(nodeOpacity(bobNode)).toBe(0.18)
    expect(linkOpacity(highlightedLink)).toBe(0.86)
    expect(linkOpacity(dimmedLink)).toBe(0.08)
    expect(highlightedLink.zIndex > dimmedLink.zIndex).toBe(true)
  })
})
