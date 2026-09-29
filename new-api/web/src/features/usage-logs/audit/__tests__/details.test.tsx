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
import {
  render,
  screen,
  within,
  waitFor,
  cleanup,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { afterEach, expect, it, vi } from 'vitest'

import zh from '@/i18n/locales/zh.json'

import type { AuditLog } from '../api'
import { AuditLogDetailsDialog } from '../components/audit-log-details-dialog'
import { buildAuditDetails } from '../lib/audit-details'

const userAgent =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 Chrome/152.0.0.0 Safari/537.36'
const entry: AuditLog = {
  event_id: 'audit-event-1',
  user_id: 1,
  username: 'root',
  actor_role: 100,
  created_at: 1788600600,
  category: 'operation',
  action: 'channel.update',
  token_ref: '',
  ip: '::1',
  user_agent: userAgent,
  method: 'PUT',
  route: '/api/channel/',
  status: 200,
  success: true,
  request_id: '20260905092951096890008268d9d6b7oeNCj3',
  content: 'Updated channel batch (ID: 42)',
  other: {
    admin_info: {
      admin_id: 1,
      admin_username: 'root',
      admin_role: 100,
      auth_method: 'session',
    },
    op: {
      action: 'channel.update',
      params: { id: 42, name: 'batch', changed_fields: [] },
    },
  },
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

it.each([
  [100, 'root'],
  [10, 'admin'],
  [1, 'user'],
] as const)('keeps role %i as %s in Chinese', async (role, label) => {
  const i18n = createInstance()
  await i18n.init({ lng: 'zh', resources: { zh } })
  const log = {
    ...entry,
    actor_role: role,
    other: {
      admin_info: { admin_username: 'literal-name', admin_role: role },
      op: { action: 'channel.update', params: { id: 42, name: 'batch' } },
    },
  }
  render(
    <I18nextProvider i18n={i18n}>
      <AuditLogDetailsDialog entry={log} />
    </I18nextProvider>
  )
  await userEvent.click(screen.getByRole('button', { name: '详情' }))
  const dialog = await screen.findByRole('dialog', { name: '日志详情' })
  expect(within(dialog).getByText(label)).toBeVisible()
  expect(within(dialog).getByText('literal-name (ID: 1)')).toBeVisible()
})

it('uses the returned authentication method for personal access records', async () => {
  const { dialog } = await openDetails({
    ...entry,
    auth_method: 'access_token',
  })
  expect(within(dialog).getByText('Access Token')).toBeVisible()
  expect(within(dialog).queryByText('Session')).not.toBeInTheDocument()
})

it.each([
  ['token.create', '创建 API 令牌「1」（ID: 11）'],
  ['token.update', '更新 API 令牌「1」（ID: 11）'],
  ['token.status_update', '更新 API 令牌「1」（ID: 11）'],
  ['token.delete', '删除 API 令牌「1」（ID: 11）'],
  ['token.delete_batch', '批量删除 API 令牌'],
  ['token.key_view', '查看 API 令牌「1」的密钥（ID: 11）'],
  ['token.key_view_batch', '批量查看 API 令牌密钥'],
])('localizes the neutral event summary for %s', async (action, summary) => {
  const i18n = createInstance()
  await i18n.init({ lng: 'zh', resources: { zh } })
  const detail = buildAuditDetails(
    {
      ...entry,
      category: 'security',
      action,
      content: '',
      other: { op: { action, params: { id: 11, name: '1' } } },
    },
    i18n.t
  )
  expect(detail.summary).toBe(summary)
})

it.each([
  [[], 'No changes'],
  [
    ['remain_quota', 'auto_groups', 'cross_group_retry'],
    'Remaining quota, Auto Group Chain, Cross-group retry',
  ],
])(
  'describes token configuration changes %j',
  async (changedFields, expected) => {
    const { dialog } = await openDetails({
      ...entry,
      category: 'security',
      action: 'token.update',
      other: {
        op: {
          action: 'token.update',
          params: { id: 42, name: 'client', changed_fields: changedFields },
        },
      },
    })
    expect(within(dialog).getAllByText(expected).length).toBeGreaterThan(0)
  }
)

it.each([
  [1, 2, 'Enabled', 'Disabled'],
  [3, 4, 'Expired', 'Exhausted'],
])(
  'formats token status transitions %i to %i',
  async (from, to, before, after) => {
    const { dialog } = await openDetails({
      ...entry,
      category: 'security',
      action: 'token.status_update',
      other: { op: { action: 'token.status_update', params: { from, to } } },
    })
    expect(
      within(dialog).getAllByText(`${before} → ${after}`).length
    ).toBeGreaterThan(0)
  }
)

it('shows a failed token batch attempt without implying completion', async () => {
  const { dialog } = await openDetails({
    ...entry,
    category: 'security',
    action: 'token.key_view_batch',
    content: '',
    success: false,
    other: {
      op: {
        action: 'token.key_view_batch',
        params: {
          total: 101,
          requested_ids: [42],
          requested_ids_truncated: true,
        },
      },
    },
  })
  expect(within(dialog).getByText('View API token keys in batch')).toBeVisible()
  expect(within(dialog).getByText('Failed')).toBeVisible()
  expect(within(dialog).getByText('Requested token IDs')).toBeVisible()
  expect(
    within(dialog).getByText(
      'Only the first 1 IDs were recorded (101 requested)'
    )
  ).toBeVisible()
  expect(within(dialog).queryByText('Count')).not.toBeInTheDocument()
})

it('shows explicit token identity before the operator and request sections', async () => {
  const { dialog } = await openDetails({
    ...entry,
    action: 'token.create',
    other: { op: { action: 'token.create', params: { id: 11, name: '1' } } },
  })
  expect(
    within(dialog).getByText('Create API token “1” (ID: 11)')
  ).toBeVisible()
  const name = within(dialog).getByText('Token Name')
  const id = within(dialog).getByText('Token ID')
  expect(name.parentElement).toHaveTextContent('1')
  expect(id.parentElement).toHaveTextContent('11')
  expect(
    name.compareDocumentPosition(within(dialog).getByText('Request')) &
      Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(within(dialog).queryByText('Target')).not.toBeInTheDocument()
})

it.each([
  [{ id: 11 }, 'Create API token (ID: 11)'],
  [{ name: '1' }, 'Create API token “1”'],
  [{}, 'Create API token · Target not recorded'],
])(
  'describes incomplete token targets without inventing values (%j)',
  async (params, summary) => {
    const { dialog } = await openDetails({
      ...entry,
      action: 'token.create',
      other: { op: { action: 'token.create', params } },
    })
    expect(within(dialog).getByText(summary)).toBeVisible()
  }
)

it('distinguishes unchanged state from missing change metadata', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en' })
  const unchanged = buildAuditDetails(
    {
      ...entry,
      action: 'token.status_update',
      other: {
        op: { action: 'token.status_update', params: { from: 1, to: 1 } },
      },
    },
    i18n.t
  )
  const missing = buildAuditDetails(
    {
      ...entry,
      action: 'token.update',
      other: { op: { action: 'token.update', params: {} } },
    },
    i18n.t
  )
  expect(unchanged.tokenOperation?.description).toBe('State unchanged: Enabled')
  expect(missing.tokenOperation?.description).toBe(
    'Field change details were not recorded'
  )
})

it('does not present recorded changes as completed when the operation failed', async () => {
  const { dialog } = await openDetails({
    ...entry,
    success: false,
    action: 'token.update',
    other: {
      op: {
        action: 'token.update',
        params: {
          id: 11,
          name: 'production',
          changed_fields: ['remain_quota'],
        },
      },
    },
  })
  expect(within(dialog).getByText('Failed')).toBeVisible()
  expect(within(dialog).queryByText('Changed Fields')).not.toBeInTheDocument()
  expect(within(dialog).queryByText(/Changed fields:/)).not.toBeInTheDocument()
})

it('renders batch IDs compactly, copies the full list and preserves an empty result', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  const { dialog } = await openDetails({
    ...entry,
    action: 'token.key_view_batch',
    other: {
      op: {
        action: 'token.key_view_batch',
        params: {
          total: 3,
          count: 0,
          requested_ids: [11, 11, 12],
          returned_ids: [],
        },
      },
    },
  })
  expect(within(dialog).getByText('11, 11, 12')).toBeVisible()
  expect(
    within(dialog).getByText('Returned token IDs').parentElement
  ).toHaveTextContent('None')
  expect(
    within(dialog).getByText('Returned keys').parentElement
  ).toHaveTextContent('0')
  await user.click(
    within(dialog).getByRole('button', { name: 'Copy Requested token IDs' })
  )
  expect(copy).toHaveBeenCalledWith('11, 11, 12')
})

async function openDetails(log: AuditLog = entry) {
  render(<AuditLogDetailsDialog entry={log} />)
  const trigger = screen.getByRole('button', { name: 'Details' })
  trigger.focus()
  await userEvent.keyboard('{Enter}')
  return {
    trigger,
    dialog: await screen.findByRole('dialog', { name: 'Log Details' }),
  }
}

it('renders the channel update as a readable summary and compact operation rows without JSON or empty token fields', async () => {
  const { dialog } = await openDetails()
  expect(
    within(dialog).getByText('Updated channel batch (ID: 42)')
  ).toBeVisible()
  expect(
    within(dialog).getByText('Field change details were not recorded')
  ).toBeVisible()
  expect(within(dialog).getByText('root')).toBeVisible()
  expect(within(dialog).getByText('Session')).toBeVisible()
  expect(within(dialog).getByText(userAgent)).toBeVisible()
  expect(within(dialog).queryByText('Token identifier')).not.toBeInTheDocument()
  expect(
    within(dialog).queryByRole('button', { name: /Expand|Collapse/ })
  ).not.toBeInTheDocument()
  expect(dialog.querySelector('pre')).not.toBeInTheDocument()
  expect(dialog).not.toHaveTextContent('changed_fields')
})

it('aligns every request field in the same label and value columns', async () => {
  const { dialog } = await openDetails({
    ...entry,
    token_ref: 'token-fingerprint',
  })
  for (const label of [
    'Method',
    'HTTP',
    'IP',
    'Client',
    'Route',
    'Request ID',
    'Token identifier',
  ]) {
    const row = within(dialog).getByText(label, { exact: true }).parentElement
    expect(row).toHaveClass(
      'grid',
      'grid-cols-[5.25rem_minmax(0,1fr)]',
      'sm:grid-cols-[7rem_minmax(0,1fr)]'
    )
  }
})

it('shows long values in full, copies the complete identifier, and restores focus when dismissed', async () => {
  const user = userEvent.setup()
  const writeText = vi
    .spyOn(navigator.clipboard, 'writeText')
    .mockResolvedValue()
  const requestId = `request-${'x'.repeat(100)}`
  const { trigger, dialog } = await openDetails({
    ...entry,
    request_id: requestId,
  })
  expect(within(dialog).getByText(requestId)).toBeVisible()
  expect(within(dialog).getByText(requestId)).not.toHaveClass('truncate')
  expect(
    within(dialog).queryByRole('button', { name: /Expand|Collapse/ })
  ).not.toBeInTheDocument()
  await user.click(
    within(dialog).getByRole('button', { name: 'Copy Request ID' })
  )
  expect(writeText).toHaveBeenCalledWith(requestId)
  expect(within(dialog).getByText(userAgent)).toBeVisible()
  await user.keyboard('{Escape}')
  await waitFor(() =>
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  )
  await waitFor(() => expect(trigger).toHaveFocus())
})

it.each([
  [
    'channel.status_update',
    { id: 42, status: 2, changed: false },
    ['Disabled', 'No'],
  ],
  [
    'channel.status_update_batch',
    { count: 0, total: 3, status: 1 },
    ['Enabled', '0 / 3'],
  ],
  ['user.create', { username: 'alice', role: 10 }, ['admin', 'alice']],
])(
  'formats known parameters for %s without losing zero or false',
  async (action, params, values) => {
    const { dialog } = await openDetails({
      ...entry,
      action,
      other: { op: { action, params } },
    })
    for (const value of values) {
      expect(
        within(dialog)
          .getAllByText(value)
          .some((node) => node.textContent === value)
      ).toBe(true)
    }
  }
)

it('translates changed field names and shows unknown nested metadata as readable fields', async () => {
  const { dialog } = await openDetails({
    ...entry,
    other: {
      op: {
        action: 'channel.update',
        params: {
          id: 42,
          name: 'batch',
          changed_fields: ['models', 'group'],
          custom: { attempts: 0, permitted: false },
        },
      },
    },
  })
  expect(within(dialog).getByText('Models, Group')).toBeVisible()
  expect(within(dialog).getByText('custom')).toBeVisible()
  expect(
    within(dialog).queryByRole('button', { name: 'custom' })
  ).not.toBeInTheDocument()
  expect(within(dialog).getByText('attempts')).toBeVisible()
  expect(within(dialog).getByText('0')).toBeVisible()
  expect(within(dialog).getByText('No')).toBeVisible()
  expect(dialog).not.toHaveTextContent('[object Object]')
  expect(dialog.querySelector('pre')).not.toBeInTheDocument()
})

it.each([null, undefined, '', '{broken', [], 42])(
  'preserves request details when metadata is missing or invalid (%s)',
  async (other) => {
    const { dialog } = await openDetails({
      ...entry,
      other: other as unknown as AuditLog['other'],
    })
    expect(within(dialog).getByText(entry.request_id)).toBeVisible()
    expect(within(dialog).getByText(entry.ip)).toBeVisible()
    expect(dialog).not.toHaveTextContent('{broken')
    expect(dialog.querySelector('pre')).not.toBeInTheDocument()
  }
)

it.each([
  [
    'login',
    'login',
    { method: 'password' },
    'Logged in successfully via Password',
  ],
  ['security', 'user.2fa_enable', {}, 'Enabled two-factor authentication'],
  ['access_token', 'access_token.request', {}, 'Access Token'],
])(
  'shows summary and result for %s records',
  async (category, action, params, summary) => {
    const { dialog } = await openDetails({
      ...entry,
      category,
      action,
      actor_role: 1,
      content: '',
      success: false,
      status: 403,
      other: { op: { action, params } },
    })
    expect(within(dialog).getAllByText(summary).length).toBeGreaterThan(0)
    expect(within(dialog).getByText('Failed')).toBeVisible()
    expect(within(dialog).getByText('403')).toBeVisible()
    expect(within(dialog).queryByText('root')).not.toBeInTheDocument()
    expect(within(dialog).getByText('user')).toBeVisible()
  }
)

it('shows the quota target and committed balances before operator information', async () => {
  const user = userEvent.setup()
  const copy = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  const { dialog } = await openDetails({
    ...entry,
    action: 'user.quota_override',
    other: {
      op: {
        action: 'user.quota_override',
        params: {
          target_user_id: 42,
          target_username: '1',
          mode: 'override',
          requested_quota: -500000,
          from: 500000,
          to: -500000,
        },
      },
    },
  })
  expect(
    within(dialog).getByText('Override quota for user “1” (ID: 42)')
  ).toBeVisible()
  const target = within(dialog).getByText('Target username')
  const operator = within(dialog).getByText('Operator')
  expect(
    target.compareDocumentPosition(operator) & Node.DOCUMENT_POSITION_FOLLOWING
  ).toBeTruthy()
  expect(within(dialog).getByText('Quota before adjustment')).toBeVisible()
  expect(within(dialog).getByText('Quota after adjustment')).toBeVisible()
  expect(within(dialog).getByText('$1')).toBeVisible()
  expect(within(dialog).getAllByText('-$1')).toHaveLength(2)
  await user.click(within(dialog).getByRole('button', { name: 'Copy User ID' }))
  expect(copy).toHaveBeenCalledWith('42')
})

it('shows failed quota attempts without claiming a balance change', async () => {
  const { dialog } = await openDetails({
    ...entry,
    success: false,
    action: 'user.quota_subtract',
    other: {
      op: {
        action: 'user.quota_subtract',
        params: {
          target_user_id: 42,
          mode: 'subtract',
          requested_quota: 500000,
          from: 1000000,
          to: 500000,
          failure_reason: 'permission_denied',
        },
      },
    },
  })
  expect(within(dialog).getByText('Failed')).toBeVisible()
  expect(within(dialog).getByText('Decrease user quota (ID: 42)')).toBeVisible()
  expect(
    within(dialog).getByText('Insufficient permission to adjust this user')
  ).toBeVisible()
  expect(within(dialog).getByText('$1')).toBeVisible()
  expect(within(dialog).queryByText('Quota before adjustment')).toBeNull()
  expect(within(dialog).queryByText('Quota after adjustment')).toBeNull()
  expect(dialog).not.toHaveTextContent('→')
})

it('distinguishes unchanged zero quota from missing or legacy balance metadata', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en' })
  const unchanged = buildAuditDetails(
    {
      ...entry,
      action: 'user.quota_override',
      other: {
        op: {
          action: 'user.quota_override',
          params: { target_user_id: 42, requested_quota: 0, from: 0, to: 0 },
        },
      },
    },
    i18n.t
  )
  expect(unchanged.summary).toBe('Override user quota (ID: 42)')
  expect(unchanged.operation?.description).toBe(
    'Requested quota: $0 · Quota unchanged · $0 → $0'
  )
  const missing = buildAuditDetails(
    {
      ...entry,
      action: 'user.quota_add',
      other: {
        op: { action: 'user.quota_add', params: { quota: '¥1.000000额度' } },
      },
    },
    i18n.t
  )
  expect(missing.summary).toContain('Target not recorded')
  expect(missing.operation?.description).toBe(
    'Requested quota: ¥1.000000额度 · Not recorded → Not recorded'
  )
  expect(missing.operation?.description).not.toContain('Quota unchanged')
  const legacy = buildAuditDetails(
    {
      ...entry,
      action: 'user.quota_override',
      other: {
        op: {
          action: 'user.quota_override',
          params: { from: 'legacy before', to: 'legacy after' },
        },
      },
    },
    i18n.t
  )
  expect(legacy.operation?.description).toContain(
    'legacy before → legacy after'
  )
})

it.each([
  [
    'redemption.delete_batch',
    '/api/redemption/batch',
    { count: 15 },
    true,
    '批量删除了 15 个兑换码',
  ],
  [
    'redemption.delete_batch',
    '/api/redemption/batch',
    { count: 0 },
    true,
    '批量删除了 0 个兑换码',
  ],
  [
    'redemption.delete',
    '/api/redemption/batch',
    {},
    true,
    '批量删除兑换码（数量未记录）',
  ],
  [
    'redemption.delete_batch',
    '/api/redemption/batch',
    {},
    false,
    '批量删除兑换码失败',
  ],
  ['redemption.delete', '/api/redemption/:id', {}, true, '删除了一个兑换码'],
])(
  'renders redemption audit %s at %s with recorded result %j',
  async (action, route, params, success, summary) => {
    const i18n = createInstance()
    await i18n.init({ lng: 'zh', resources: { zh } })
    const detail = buildAuditDetails(
      {
        ...entry,
        action,
        route,
        method: route.endsWith('/batch') ? 'POST' : 'DELETE',
        success,
        other: { op: { action, params } },
      },
      i18n.t
    )
    expect(detail.summary).toBe(summary)
  }
)

it('shows the affected count separately from requested redemption IDs', async () => {
  const i18n = createInstance()
  await i18n.init({ lng: 'en', resources: {} })
  const detail = buildAuditDetails(
    {
      ...entry,
      action: 'redemption.delete_batch',
      route: '/api/redemption/batch',
      method: 'POST',
      other: {
        op: {
          action: 'redemption.delete_batch',
          params: {
            count: 2,
            total: 4,
            requested_redemption_ids: [11, 12, 11, 999],
          },
        },
      },
    },
    i18n.t
  )
  expect(detail.summary).toBe('Batch deleted 2 redemption codes')
  expect(detail.fields).toEqual(
    expect.arrayContaining([
      { label: 'Count', value: 2 },
      { label: 'Total', value: 4 },
      { label: 'Requested redemption code IDs', value: [11, 12, 11, 999] },
    ])
  )
})
