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
import type { StatusBadgeProps } from '@/components/status-badge'
import {
  BILLING_PRICING_VARS,
  normalizeTierLabel,
  parseTiersFromExpr,
  splitBillingExprAndRequestRules,
  type ParsedTier,
} from '@/features/pricing/lib/billing-expr'

import type { UsageLog } from '../data/schema'
import type { LogOtherData } from '../types'
import { buildQuotaAuditOperation } from './quota-audit-operation'

export { normalizeTierLabel }

const PARAM_OVERRIDE_ACTION_MAP: Record<string, string> = {
  set: 'Set',
  delete: 'Delete',
  copy: 'Copy',
  move: 'Move',
  append: 'Append',
  prepend: 'Prepend',
  trim_prefix: 'Trim Prefix',
  trim_suffix: 'Trim Suffix',
  ensure_prefix: 'Ensure Prefix',
  ensure_suffix: 'Ensure Suffix',
  trim_space: 'Trim Space',
  to_lower: 'To Lower',
  to_upper: 'To Upper',
  replace: 'Replace',
  regex_replace: 'Regex Replace',
  set_header: 'Set Header',
  delete_header: 'Delete Header',
  copy_header: 'Copy Header',
  move_header: 'Move Header',
  pass_headers: 'Pass Headers',
  sync_fields: 'Sync Fields',
  return_error: 'Return Error',
}

/**
 * Get localized label for a param override action
 */
export function getParamOverrideActionLabel(
  action: string,
  t: (key: string) => string
): string {
  const key = PARAM_OVERRIDE_ACTION_MAP[action.toLowerCase()]
  return key ? t(key) : action
}

/**
 * Parse a param override audit line into action and content
 */
export function parseAuditLine(
  line: string
): { action: string; content: string } | null {
  if (typeof line !== 'string') return null
  const firstSpace = line.indexOf(' ')
  if (firstSpace <= 0) return { action: line, content: line }
  return {
    action: line.slice(0, firstSpace),
    content: line.slice(firstSpace + 1),
  }
}

/**
 * Check if the log is a violation fee log
 */
export function isViolationFeeLog(other: LogOtherData | null): boolean {
  if (!other) return false
  return (
    other.violation_fee === true ||
    Boolean(other.violation_fee_code) ||
    Boolean(other.violation_fee_marker)
  )
}

function isPositiveFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value > 0
}

function hasLegacySearchSurcharge(
  enabled: boolean | undefined,
  count: number | undefined,
  price: number | undefined
): boolean {
  return (
    enabled === true &&
    isPositiveFiniteNumber(count) &&
    isPositiveFiniteNumber(price)
  )
}

/**
 * Check whether a consume log includes an actual tool-call surcharge.
 * Structured surcharge items cover current logs, while the legacy fields keep
 * historical Web Search, File Search, and Image Generation logs visible.
 */
export function hasToolSurcharge(other: LogOtherData | null): boolean {
  if (!other) return false

  const hasStructuredSurcharge =
    Array.isArray(other.tool_surcharges) &&
    other.tool_surcharges.some(
      (item) =>
        typeof item?.name === 'string' &&
        item.name.trim() !== '' &&
        isPositiveFiniteNumber(item.count) &&
        isPositiveFiniteNumber(item.price)
    )
  if (hasStructuredSurcharge) return true

  if (
    hasLegacySearchSurcharge(
      other.web_search,
      other.web_search_call_count,
      other.web_search_price
    )
  ) {
    return true
  }

  if (
    hasLegacySearchSurcharge(
      other.file_search,
      other.file_search_call_count,
      other.file_search_price
    )
  ) {
    return true
  }

  return (
    other.image_generation_call === true &&
    isPositiveFiniteNumber(other.image_generation_call_price)
  )
}

/**
 * Parse the 'other' field from JSON string to object
 */
export function parseLogOther(other: string): LogOtherData | null {
  if (!other) return null
  try {
    return JSON.parse(other) as LogOtherData
  } catch (error) {
    // eslint-disable-next-line no-console
    console.error('Failed to parse log other field:', error)
    return null
  }
}

export function getReasoningEffortVariant(
  effort: string | undefined
): StatusBadgeProps['variant'] {
  switch (effort?.trim().toLowerCase()) {
    case 'max':
    case 'xhigh':
    case 'high':
      return 'orange'
    case 'medium':
      return 'yellow'
    case 'low':
    case 'minimal':
      return 'green'
    case 'none':
    default:
      return 'grey'
  }
}

/**
 * Get time color based on duration (in seconds)
 */
export function getTimeColor(
  seconds: number
): 'success' | 'warning' | 'danger' {
  if (seconds < 10) return 'success'
  if (seconds < 30) return 'warning'
  return 'danger'
}

/**
 * Get first-response-token color based on latency (in seconds)
 */
export function getFirstResponseTimeColor(
  seconds: number
): 'success' | 'warning' | 'danger' {
  if (seconds < 5) return 'success'
  if (seconds < 10) return 'warning'
  return 'danger'
}

/**
 * Get throughput color based on generated tokens per second
 */
export function getThroughputColor(
  tokensPerSecond: number
): 'success' | 'warning' | 'danger' {
  if (tokensPerSecond >= 30) return 'success'
  if (tokensPerSecond >= 15) return 'warning'
  return 'danger'
}

/**
 * Get response color using throughput only when enough output tokens exist.
 */
export function getResponseTimeColor(
  seconds: number,
  completionTokens: number
): 'success' | 'warning' | 'danger' {
  if (completionTokens < 100 || seconds <= 0) return getTimeColor(seconds)
  return getThroughputColor(completionTokens / seconds)
}

/**
 * Format model name with mapping indicator
 */
export function formatModelName(log: UsageLog): {
  name: string
  isMapped: boolean
  actualModel?: string
  responseModel?: LogOtherData['response_model']
} {
  const other = parseLogOther(log.other)
  const isMapped = !!(
    other?.is_model_mapped &&
    other?.upstream_model_name &&
    other.upstream_model_name !== ''
  )

  return {
    name: log.model_name,
    isMapped,
    actualModel: isMapped ? other.upstream_model_name : undefined,
    responseModel: other?.response_model,
  }
}

/**
 * Decode a base64-encoded billing expression. Safely returns an empty string
 * when the input is missing or malformed (e.g. legacy logs without expr_b64).
 */
export function decodeBillingExprB64(exprB64: string | undefined): string {
  if (!exprB64) return ''
  try {
    const binaryString =
      typeof window !== 'undefined'
        ? window.atob(exprB64)
        : Buffer.from(exprB64, 'base64').toString('binary')
    const bytes = new Uint8Array(binaryString.length)

    for (let i = 0; i < binaryString.length; i++) {
      bytes[i] = binaryString.charCodeAt(i)
    }

    if (typeof TextDecoder !== 'undefined') {
      return new TextDecoder().decode(bytes)
    }

    return decodeURIComponent(
      Array.prototype.map
        .call(bytes, (byte: number) => `%${byte.toString(16).padStart(2, '0')}`)
        .join('')
    )
  } catch {
    return ''
  }
}

/**
 * Resolve which parsed tier corresponds to the matched_tier label in a log
 * entry. Missing or unknown labels do not fall back to another tier because
 * that would display guessed unit prices.
 */
export function resolveMatchedTier(
  tiers: ParsedTier[],
  matchedLabel: string | undefined
): ParsedTier | null {
  if (tiers.length === 0) return null
  if (!matchedLabel) return null
  const found = tiers.find((tier) => {
    const l1 = normalizeTierLabel(tier.label)
    const l2 = normalizeTierLabel(matchedLabel)
    return l1 === l2 && l1 !== ''
  })
  return found || null
}

/**
 * Tiered pricing summary derived from an `other` log payload using the
 * billing-expression library. Returns null when the entry is not a tiered
 * billing log or the expression failed to parse.
 */
export interface TieredBillingSummary {
  tiers: ParsedTier[]
  tier: ParsedTier
  priceEntries: Array<{
    field: string
    shortLabel: string
    price: number
    unit?: 'request' | 'image'
  }>
}

/**
 * Whether the request payload reports any cache-related token usage. Used to
 * suppress cache pricing rows from the tiered breakdown when the request did
 * not exercise the cache path.
 */
export function hasAnyCacheTokens(
  other: LogOtherData | null | undefined
): boolean {
  if (!other) return false
  return (
    (other.cache_tokens || 0) > 0 ||
    (other.image_cache_tokens || 0) > 0 ||
    (other.cache_creation_tokens || 0) > 0 ||
    (other.cache_creation_tokens_5m || 0) > 0 ||
    (other.cache_creation_tokens_1h || 0) > 0
  )
}

export function getTieredBillingSummary(
  other: LogOtherData | null
): TieredBillingSummary | null {
  if (!other || other.billing_mode !== 'tiered_expr') return null
  const exprStr = decodeBillingExprB64(other.expr_b64)
  if (!exprStr) return null
  const tiers = parseTiersFromExpr(
    splitBillingExprAndRequestRules(exprStr).billingExpr
  )
  const tier = resolveMatchedTier(tiers, other.matched_tier)
  if (
    other.billing_unit === 'request' &&
    typeof other.fixed_price === 'number' &&
    Number.isFinite(other.fixed_price) &&
    other.fixed_price >= 0
  ) {
    const fixedPrice = other.fixed_price
    const actualTier = tiers.find(
      (entry) =>
        normalizeTierLabel(entry.label) ===
          normalizeTierLabel(other.matched_tier) &&
        entry.billingUnit === 'request' &&
        entry.fixedPrice === fixedPrice
    ) ?? {
      label: other.matched_tier || '',
      conditions: [],
      billingUnit: 'request' as const,
      fixedPrice,
    }
    return {
      tiers,
      tier: actualTier,
      priceEntries: [
        {
          field: 'fixedPrice',
          shortLabel:
            other.image_count !== undefined ? 'Per image' : 'Per-call',
          price: fixedPrice,
          unit: other.image_count !== undefined ? 'image' : 'request',
        },
      ],
    }
  }
  if (!tier) return null
  if (tier.billingUnit === 'request' && typeof tier.fixedPrice === 'number') {
    return {
      tiers,
      tier,
      priceEntries: [
        {
          field: 'fixedPrice',
          shortLabel: tier.imageCount ? 'Per image' : 'Per-call',
          price: tier.fixedPrice,
          unit: tier.imageCount ? 'image' : 'request',
        },
      ],
    }
  }

  const cacheTokensPresent = hasAnyCacheTokens(other)

  const priceEntries: TieredBillingSummary['priceEntries'] = []
  for (const v of BILLING_PRICING_VARS) {
    if (!v.field) continue
    if (v.group === 'cache' && !cacheTokensPresent) continue
    const raw = tier[v.field as keyof ParsedTier]
    const price = Number(raw)
    if (Number.isFinite(price) && price >= 0) {
      priceEntries.push({
        field: v.field,
        shortLabel: v.shortLabel,
        price,
      })
    }
  }
  return { tiers, tier, priceEntries }
}

/**
 * Calculate duration and return formatted result with color variant
 * @param submitTime - Submit timestamp
 * @param finishTime - Finish timestamp
 * @param unit - Unit of the timestamps ('seconds' or 'milliseconds')
 */
export function formatDuration(
  submitTime?: number,
  finishTime?: number,
  unit: 'seconds' | 'milliseconds' = 'milliseconds'
): { durationSec: number; variant: StatusBadgeProps['variant'] } | null {
  if (!submitTime || !finishTime) return null

  const durationSec =
    unit === 'milliseconds'
      ? (finishTime - submitTime) / 1000
      : finishTime - submitTime

  return { durationSec, variant: durationSec > 60 ? 'red' : 'green' }
}

/**
 * Maps a language-independent audit/login operation `action` to an i18n
 * template string (the template itself is the i18n key, with {{placeholders}}).
 *
 * The backend stores only `action` + structured `params` in `other.op`; the UI
 * renders localized content at display time so audit/login logs are fully
 * translatable instead of being frozen to whatever language was written to DB.
 */
const AUDIT_TEMPLATES: Record<string, string> = {
  'token.create': 'API token creation',
  'token.update': 'API token configuration update',
  'token.status_update': 'API token status update',
  'token.delete': 'API token deletion',
  'token.delete_batch': 'API token batch deletion',
  'token.key_view': 'API token key access',
  'token.key_view_batch': 'API token batch key access',
  'access_token.generate': 'Generated a system access token',
  'access_token.revoke': 'Revoked the system access token',
  'user.2fa_setup': 'Started two-factor authentication setup',
  'user.2fa_enable': 'Enabled two-factor authentication',
  'user.2fa_disable_self': 'Disabled two-factor authentication',
  'user.2fa_backup_codes': 'Regenerated two-factor backup codes',
  'user.security_verify': 'Completed security verification',
  'user.password_change': 'Account password change',
  'user.binding_start': 'Account binding request',
  'user.binding_bind': 'Account binding',
  'user.binding_unbind': 'Account unlinking',
  'user.email_binding_resend': 'Email confirmation code resend',

  login: 'Logged in successfully via {{method}}',
  // User management
  'user.create': 'Created user {{username}} (role {{role}})',
  'user.update': 'Updated user {{username}} (ID: {{id}})',
  'user.delete': 'Deleted user {{username}} (ID: {{id}})',
  'user.account_delete': 'Account deletion',
  'user.manage': 'Performed {{action}} on user {{username}} (ID: {{id}})',
  'user.quota_add': 'Increased user quota by {{quota}}',
  'user.quota_subtract': 'Decreased user quota by {{quota}}',
  'user.quota_override': 'Overrode user quota from {{from}} to {{to}}',
  'user.binding_clear': 'Cleared {{bindingType}} binding for user {{username}}',
  'user.2fa_disable': 'Force-disabled two-factor authentication for the user',
  'user.passkey_register': 'Registered a passkey',
  'user.passkey_delete': 'Deleted a passkey',
  'user.topup_complete': 'Completed top-up order for the user',
  'user.reset_passkey': 'Reset the user passkey',
  'user.oauth_unbind': 'Removed an OAuth binding for the user',
  // System settings
  'option.update': 'Updated system setting {{key}}',
  'option.passkey_domains':
    'Updated Passkey domains: removed {{domains}}; affected {{known}}; unknown {{unknown}}',
  'option.passkey_domains_confirmed':
    'Confirmed removal of Passkey domains: {{domains}}; affected {{known}}; unknown {{unknown}}',
  'option.passkey_domains_blocked':
    'Passkey domain change blocked: {{domains}}; affected {{known}}; unknown {{unknown}}',
  'option.passkey_domains_failed': 'Passkey domain update failed',
  'option.payment_compliance': 'Confirmed payment compliance',
  'option.reset_ratio': 'Reset model ratios',
  'option.clear_affinity_cache': 'Cleared channel affinity cache',
  // Custom OAuth
  'custom_oauth.create': 'Created a custom OAuth provider',
  'custom_oauth.update': 'Updated a custom OAuth provider',
  'custom_oauth.delete': 'Deleted a custom OAuth provider',
  // Performance / cache
  'performance.clear_disk_cache': 'Cleared disk cache',
  'performance.gc': 'Triggered garbage collection',
  'performance.clear_logs': 'Cleared log files',
  // Channel
  'channel.create': 'Created channel {{name}} (type {{type}}, count {{count}})',
  'channel.update': 'Updated channel {{name}} (ID: {{id}})',
  'channel.status_update': 'Updated channel status (ID: {{id}})',
  'channel.status_update_batch':
    'Batch updated channel status ({{count}}/{{total}} changed)',
  'channel.delete': 'Deleted channel {{name}} (ID: {{id}})',
  'channel.delete_batch': 'Batch deleted {{count}} channels',
  'channel.delete_disabled': 'Deleted all disabled channels ({{count}})',
  'channel.key_view': 'Viewed channel key {{name}} (ID: {{id}})',
  'channel.tag_disable': 'Disabled channels with tag {{tag}}',
  'channel.tag_enable': 'Enabled channels with tag {{tag}}',
  'channel.tag_edit': 'Edited channels with tag {{tag}}',
  'channel.tag_batch_set': 'Batch set tag for {{count}} channels',
  'channel.copy':
    'Copied channel (source ID: {{sourceId}}) to {{name}} (new ID: {{id}})',
  'channel.multi_key_manage':
    'Multi-key management {{action}} on channel (ID: {{id}})',
  'channel.upstream_apply':
    'Applied upstream model changes to channel (ID: {{id}})',
  'channel.upstream_apply_all':
    'Applied upstream model changes to {{count}} channels',
  // Redemption codes
  'redemption.create':
    'Created {{count}} redemption codes named {{name}} ({{quota}} each)',
  'redemption.update': 'Updated a redemption code',
  'redemption.delete': 'Deleted a redemption code',
  'redemption.delete_invalid': 'Deleted invalid redemption codes',
  // Prefill groups
  'prefill_group.create': 'Created a prefill group',
  'prefill_group.update': 'Updated a prefill group',
  'prefill_group.delete': 'Deleted a prefill group',
  // Vendors
  'vendor.create': 'Created a vendor',
  'vendor.update': 'Updated a vendor',
  'vendor.delete': 'Deleted a vendor',
  // Model metadata
  'model.create': 'Created a model',
  'model.update': 'Updated a model',
  'model.delete': 'Deleted a model',
  'model.sync_upstream': 'Synced upstream models',
  // Deployments
  'deployment.create': 'Created a deployment',
  'deployment.update': 'Updated a deployment',
  'deployment.delete': 'Deleted a deployment',
  // Subscriptions
  'subscription.plan_create': 'Created a subscription plan',
  'subscription.plan_update': 'Updated a subscription plan',
  'subscription.bind': 'Bound a subscription',
  // Logs
  'log.clear': 'Cleared historical logs',
  'log.cleanup_start': 'Log cleanup task started.',
  // Generic middleware fallback
  generic: '{{method}} {{route}}',
}

/**
 * Render the localized content of an operation log from its structured
 * `other.op` descriptor. Returns null when the log has no recognized action,
 * letting callers fall back to the raw `content` field.
 */
export function renderAuditContent(
  other: LogOtherData | null | undefined,
  t: (key: string, opts?: Record<string, unknown>) => string
): string | null {
  const op = other?.op
  if (!op?.action) return null
  if (
    op.action === 'redemption.delete_batch' ||
    (op.action === 'redemption.delete' &&
      other?.audit_info?.route === '/api/redemption/batch')
  ) {
    if (other?.audit_info?.success === false) {
      return t('Failed to batch delete redemption codes')
    }
    const count = op.params?.count
    if (
      typeof count === 'number' &&
      Number.isSafeInteger(count) &&
      count >= 0
    ) {
      return t('Batch deleted {{count}} redemption codes', { count })
    }
    return t('Batch deleted redemption codes (count not recorded)')
  }
  const template = AUDIT_TEMPLATES[op.action]
  if (!template) return null
  const quotaOperation = buildQuotaAuditOperation(
    op.action,
    op.params ?? {},
    other?.audit_info?.success !== false,
    t
  )
  if (quotaOperation) {
    return `${quotaOperation.summary} · ${quotaOperation.description}`
  }
  const params = { ...op.params }
  return t(template, params)
}
