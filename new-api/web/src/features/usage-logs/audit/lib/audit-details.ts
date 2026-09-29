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
import type { TFunction } from 'i18next'

import { loginMethodLabel } from '@/features/security/components/login-session-utils'
import { ROLE } from '@/lib/roles'

import { renderAuditContent } from '../../lib/format'
import { buildQuotaAuditOperation } from '../../lib/quota-audit-operation'
import type { LogOtherData } from '../../types'
import type { AuditLog } from '../api'

const AUDIT_ROLE_NAMES: Record<number, string> = {
  [ROLE.GUEST]: 'guest',
  [ROLE.USER]: 'user',
  [ROLE.ADMIN]: 'admin',
  [ROLE.SUPER_ADMIN]: 'root',
}

const TOKEN_AUDIT_OPERATIONS: Record<
  string,
  { labelKey: string; namedKey?: string }
> = {
  'token.create': {
    labelKey: 'Create API token',
    namedKey: 'Create API token “{{name}}”',
  },
  'token.update': {
    labelKey: 'Update API token',
    namedKey: 'Update API token “{{name}}”',
  },
  'token.status_update': {
    labelKey: 'Update API token',
    namedKey: 'Update API token “{{name}}”',
  },
  'token.delete': {
    labelKey: 'Delete API token',
    namedKey: 'Delete API token “{{name}}”',
  },
  'token.key_view': {
    labelKey: 'View API token key',
    namedKey: 'View key for API token “{{name}}”',
  },
  'token.delete_batch': { labelKey: 'Batch delete API tokens' },
  'token.key_view_batch': { labelKey: 'View API token keys in batch' },
}

export type AuditDetailField = {
  label: string
  value: unknown
  copyable?: boolean
}

export function isAuditDetailObject(
  value: unknown
): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
}

export function auditFieldLabel(key: string, t: TFunction): string {
  switch (key) {
    case 'status':
      return t('Status')
    case 'role':
      return t('Role')
    case 'changed_fields':
      return t('Changed Fields')
    case 'changed':
      return t('State changed')
    case 'count':
      return t('Count')
    case 'total':
      return t('Total')
    case 'requested_redemption_ids':
      return t('Requested redemption code IDs')
    case 'requested_ids':
      return t('Requested token IDs')
    case 'returned_ids':
      return t('Returned token IDs')
    case 'requested_ids_truncated':
      return t('Requested token IDs truncated')
    case 'expired_time':
      return t('Expiration Time')
    case 'remain_quota':
      return t('Remaining quota')
    case 'unlimited_quota':
      return t('Unlimited Quota')
    case 'model_limits_enabled':
      return t('Model limits enabled')
    case 'model_limits':
      return t('Model Limits')
    case 'allow_ips':
      return t('IP Whitelist (supports CIDR)')
    case 'auto_groups':
      return t('Auto Group Chain')
    case 'cross_group_retry':
      return t('Cross-group retry')
    case 'sourceId':
      return t('Source ID')
    case 'id':
      return 'ID'
    case 'name':
      return t('Name')
    case 'username':
      return t('Username')
    case 'target_user_id':
      return t('User ID')
    case 'plan_id':
      return t('Plan ID')
    case 'plan_title':
      return t('Plan Title')
    case 'reset_count':
      return t('Reset Count')
    case 'user_count':
      return t('User Count')
    case 'advance_reset_time':
      return t('Advance next reset time')
    case 'bindingType':
      return t('Binding Type')
    case 'from':
      return t('Previous value')
    case 'to':
      return t('New value')
    case 'action':
      return t('Operation')
    case 'method':
      return t('Authentication Method')
    case 'key':
      return t('Key')
    case 'tag':
      return t('Tag')
    case 'group':
      return t('Group')
    case 'models':
      return t('Models')
    case 'type':
      return t('Type')
    case 'base_url':
      return t('Base URL')
    case 'quota':
      return t('Quota')
    case 'admin_info':
      return t('Operator Admin')
    case 'audit_info':
      return t('Request')
    default:
      return key
  }
}

function buildTokenAuditOperation(
  action: string,
  params: Record<string, unknown>,
  success: boolean,
  t: TFunction
) {
  const operation = TOKEN_AUDIT_OPERATIONS[action]
  if (!operation) return null

  const fields: AuditDetailField[] = []
  let headline = t(operation.labelKey)
  let summary = headline
  let identifier = ''
  let description = ''

  if (operation.namedKey) {
    const name =
      typeof params.name === 'string' && params.name.trim() ? params.name : ''
    let id = ''
    if (typeof params.id === 'number' && Number.isFinite(params.id)) {
      id = String(params.id)
    } else if (typeof params.id === 'string' && params.id.trim()) {
      id = params.id
    }
    if (name) {
      headline = t(operation.namedKey, { name })
      fields.push({ label: t('Token Name'), value: name })
    }
    summary = headline
    if (id) {
      identifier = t('(ID: {{id}})', { id })
      summary = t('{{operation}} (ID: {{id}})', { operation: headline, id })
      fields.push({ label: t('Token ID'), value: id, copyable: true })
    }
    if (!name && !id) {
      headline = `${headline} · ${t('Target not recorded')}`
      summary = headline
      fields.push({ label: t('Target'), value: t('Target not recorded') })
    }
  }

  if (success && action === 'token.update') {
    const changed = params.changed_fields
    description = t('Field change details were not recorded')
    let changes = description
    if (
      Array.isArray(changed) &&
      changed.every((field) => typeof field === 'string')
    ) {
      changes = changed.length
        ? changed.map((field) => auditFieldLabel(field, t)).join(', ')
        : t('No changes')
      description = changed.length
        ? t('Changed fields: {{fields}}', { fields: changes })
        : changes
    }
    fields.push({ label: t('Changed Fields'), value: changes })
  }

  if (success && action === 'token.status_update') {
    const statuses: Record<string, string> = {
      '1': t('Enabled'),
      '2': t('Disabled'),
      '3': t('Expired'),
      '4': t('Exhausted'),
    }
    const states = [params.from, params.to].map((value) => {
      if (typeof value !== 'number' && typeof value !== 'string') return ''
      const status = String(value)
      return statuses[status] || status
    })
    if (!states[0] && !states[1]) {
      description = t('Field change details were not recorded')
    } else if (states[0] && states[0] === states[1]) {
      description = t('State unchanged: {{status}}', { status: states[0] })
    } else {
      description = `${states[0] || t('Not recorded')} → ${states[1] || t('Not recorded')}`
    }
    fields.push({ label: t('Status change'), value: description })
  }

  if (!operation.namedKey) {
    const total =
      typeof params.total === 'number' &&
      Number.isFinite(params.total) &&
      params.total >= 0
        ? params.total
        : undefined
    const processed =
      success &&
      typeof params.count === 'number' &&
      Number.isFinite(params.count) &&
      params.count >= 0
        ? params.count
        : undefined
    if (total !== undefined) {
      description = t('Requested: {{total}}', { total })
      fields.push({ label: t('Requested items'), value: total })
    }
    if (processed !== undefined) {
      if (action === 'token.delete_batch') {
        description =
          total === undefined
            ? t('Deleted: {{processed}}', { processed })
            : t('Requested: {{total}} · Deleted: {{processed}}', {
                total,
                processed,
              })
        fields.push({ label: t('Deleted tokens'), value: processed })
      } else {
        description =
          total === undefined
            ? t('Returned: {{processed}}', { processed })
            : t('Requested: {{total}} · Returned: {{processed}}', {
                total,
                processed,
              })
        fields.push({ label: t('Returned keys'), value: processed })
      }
    }
    for (const key of ['requested_ids', 'returned_ids']) {
      if (key === 'returned_ids' && !success) continue
      const ids = params[key]
      if (ids === undefined) continue
      const valid =
        Array.isArray(ids) &&
        ids.every((id) => typeof id === 'number' || typeof id === 'string')
      let value = t('Not recorded')
      if (valid) value = ids.length ? ids.join(', ') : t('None')
      fields.push({
        label: auditFieldLabel(key, t),
        value,
        copyable: valid && ids.length > 0,
      })
      if (
        key === 'requested_ids' &&
        valid &&
        params.requested_ids_truncated === true
      ) {
        const note =
          total === undefined
            ? t('Only the first {{shown}} IDs were recorded', {
                shown: ids.length,
              })
            : t(
                'Only the first {{shown}} IDs were recorded ({{total}} requested)',
                { shown: ids.length, total }
              )
        fields.push({ label: t('Note'), value: note })
      }
    }
  }

  return { headline, summary, identifier, description, fields }
}

export function buildAuditDetails(entry: AuditLog, t: TFunction) {
  const metadata = isAuditDetailObject(entry.other) ? entry.other : {}
  const metadataUnavailable =
    entry.other != null && !isAuditDetailObject(entry.other)
  const op = isAuditDetailObject(metadata.op) ? metadata.op : {}
  const action = typeof op.action === 'string' ? op.action : entry.action || ''
  const params = isAuditDetailObject(op.params) ? { ...op.params } : {}
  const tokenOperation = buildTokenAuditOperation(
    action,
    params,
    entry.success,
    t
  )
  const quotaOperation = buildQuotaAuditOperation(
    action,
    params,
    entry.success,
    t
  )
  const operation = tokenOperation ?? quotaOperation
  const summaryParams: NonNullable<NonNullable<LogOtherData['op']>['params']> =
    {}
  for (const [key, value] of Object.entries(params)) {
    if (
      typeof value === 'string' ||
      typeof value === 'number' ||
      typeof value === 'boolean'
    ) {
      summaryParams[key] = value
    } else if (
      Array.isArray(value) &&
      value.every((item) => typeof item === 'string')
    ) {
      summaryParams[key] = value
    }
  }
  if (typeof summaryParams.method === 'string') {
    summaryParams.method = loginMethodLabel(summaryParams.method, t)
  }
  if (
    typeof summaryParams.role === 'number' &&
    [0, 1, 10, 100].includes(summaryParams.role)
  ) {
    summaryParams.role = AUDIT_ROLE_NAMES[summaryParams.role]
  }

  let fallback = t('Operation audit')
  if (entry.category === 'login') fallback = t('Login')
  if (entry.category === 'security') fallback = t('Account security')
  if (entry.category === 'access_token') fallback = t('Access Token')
  const summary =
    renderAuditContent(
      {
        op: { action, params: summaryParams },
        audit_info: {
          method: entry.method,
          route: entry.route,
          path: entry.route,
          status: entry.status,
          success: entry.success,
        },
      },
      t
    ) || (entry.content && entry.content !== action ? entry.content : fallback)
  const admin = isAuditDetailObject(metadata.admin_info)
    ? metadata.admin_info
    : {}
  const actorName =
    typeof admin.admin_username === 'string'
      ? admin.admin_username
      : entry.username
  const actorId =
    typeof admin.admin_id === 'number' || typeof admin.admin_id === 'string'
      ? admin.admin_id
      : entry.user_id
  let actor = actorName
  if (actorId) {
    actor = actorName ? `${actorName} (ID: ${actorId})` : `ID: ${actorId}`
  }
  let actorRole = ''
  if ([1, 10, 100].includes(entry.actor_role)) {
    actorRole = AUDIT_ROLE_NAMES[entry.actor_role]
  }
  const authMethod =
    entry.auth_method ||
    (typeof admin.auth_method === 'string' ? admin.auth_method : '')
  let authentication = authMethod
  if (authMethod === 'session') authentication = t('Session')
  else if (authMethod === 'access_token') authentication = t('Access Token')
  else if (authMethod) authentication = loginMethodLabel(authMethod, t)

  let targetName = ''
  if (typeof params.name === 'string') targetName = params.name
  else if (typeof params.username === 'string') targetName = params.username
  const targetId =
    typeof params.id === 'number' || typeof params.id === 'string'
      ? params.id
      : undefined
  let target = targetName
  if (targetId !== undefined) {
    target = targetName ? `${targetName} (ID: ${targetId})` : `ID: ${targetId}`
  }
  if (target) {
    delete params.id
    delete params.name
    delete params.username
  }

  const fields: AuditDetailField[] = []
  if (
    Array.isArray(params.changed_fields) &&
    params.changed_fields.every((field) => typeof field === 'string')
  ) {
    let changes = t('Field change details were not recorded')
    if (params.changed_fields.length) {
      changes = params.changed_fields
        .map((field) => auditFieldLabel(field, t))
        .join(', ')
    }
    fields.push({
      label: t('Changed Fields'),
      value: changes,
    })
    delete params.changed_fields
  }
  if (action.startsWith('channel.') && typeof params.status === 'number') {
    if (params.status === 1) params.status = t('Enabled')
    else if (params.status === 2) params.status = t('Disabled')
    else if (params.status === 3) params.status = t('Auto Disabled')
  }
  if (
    typeof params.role === 'number' &&
    [0, 1, 10, 100].includes(params.role)
  ) {
    params.role = AUDIT_ROLE_NAMES[params.role]
  }
  if (typeof params.method === 'string') {
    params.method = loginMethodLabel(params.method, t)
  }
  if (
    action === 'channel.status_update_batch' &&
    typeof params.count === 'number' &&
    typeof params.total === 'number'
  ) {
    fields.push({
      label: t('Changed / Total'),
      value: `${params.count} / ${params.total}`,
    })
    delete params.count
    delete params.total
  }
  for (const [key, value] of Object.entries(params)) {
    fields.push({ label: auditFieldLabel(key, t), value })
  }
  if (
    typeof metadata.login_method === 'string' &&
    params.method === undefined
  ) {
    fields.push({
      label: t('Login Method'),
      value: loginMethodLabel(metadata.login_method, t),
    })
  }

  const extra = { ...metadata }
  delete extra.op
  delete extra.admin_info
  delete extra.audit_info
  delete extra.login_method
  delete extra.user_agent
  const adminExtra = { ...admin }
  for (const key of [
    'admin_id',
    'admin_username',
    'admin_role',
    'auth_method',
  ]) {
    delete adminExtra[key]
  }
  if (Object.keys(adminExtra).length) extra.admin_info = adminExtra
  if (isAuditDetailObject(metadata.audit_info)) {
    const auditExtra = { ...metadata.audit_info }
    for (const key of ['method', 'route', 'path', 'status', 'success']) {
      delete auditExtra[key]
    }
    if (Object.keys(auditExtra).length) extra.audit_info = auditExtra
  }
  return {
    summary: operation?.summary ?? summary,
    tokenOperation,
    quotaOperation,
    operation,
    actor,
    actorRole,
    target,
    authentication,
    fields,
    extra,
    metadataUnavailable,
  }
}
