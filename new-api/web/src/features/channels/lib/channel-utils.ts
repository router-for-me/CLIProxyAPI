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
import { formatCurrencyFromUSD, formatQuotaWithCurrency } from '@/lib/currency'
import { formatTimestampToDate } from '@/lib/format'

import {
  CHANNEL_STATUS_CONFIG,
  CHANNEL_TYPES,
  MULTI_KEY_STATUS_CONFIG,
  RESPONSE_TIME_CONFIG,
  RESPONSE_TIME_THRESHOLDS,
  TYPE_TO_KEY_PROMPT,
} from '../constants'
import type { Channel, ChannelSettings, ChannelOtherSettings } from '../types'

// ============================================================================
// Channel Type Utilities
// ============================================================================

/**
 * Get human-readable channel type label
 */
export function getChannelTypeLabel(type: number): string {
  return CHANNEL_TYPES[type as keyof typeof CHANNEL_TYPES] || 'Unknown'
}

/**
 * Get channel type icon name for getLobeIcon
 * Maps channel types to Lobe icon names using type number (language-independent)
 */
export function getChannelTypeIcon(type: number): string {
  const TYPE_TO_ICON: Record<number, string> = {
    // OpenAI family
    1: 'OpenAI', // OpenAI
    6: 'OpenAI', // OpenAIMax
    7: 'OpenAI', // OhMyGPT
    8: 'OpenAI', // Custom
    58: 'NewAPI', // Advanced Custom
    59: 'Sub2API', // Sub2API
    60: 'NewAPI', // New API
    62: 'Vllm', // vLLM
    63: 'SGLang', // SGLang
    3: 'Azure', // Azure

    // Anthropic
    14: 'Claude', // Anthropic

    // Google family
    24: 'Gemini', // Gemini
    11: 'Google', // PaLM
    41: 'Gemini', // Vertex AI

    // Cloud providers
    33: 'Aws', // AWS
    39: 'Cloudflare', // Cloudflare

    // Chinese providers
    15: 'Baidu', // Baidu
    46: 'Baidu', // Baidu V2
    16: 'Zhipu', // Zhipu
    26: 'Zhipu', // Zhipu V4
    17: 'Qwen', // Ali
    18: 'Spark', // Xunfei
    23: 'Hunyuan', // Tencent
    19: 'Ai360', // 360
    25: 'Moonshot', // Moonshot
    31: 'Yi', // LingYiWanWu
    35: 'Minimax', // MiniMax
    45: 'Volcengine', // VolcEngine

    // Other AI providers
    4: 'Ollama', // Ollama
    27: 'Perplexity', // Perplexity
    34: 'Cohere', // Cohere
    42: 'Mistral', // Mistral
    43: 'DeepSeek', // DeepSeek
    48: 'XAI', // xAI
    49: 'Coze', // Coze
    40: 'SiliconCloud', // SiliconFlow
    44: 'OpenAI', // MokaAI
    20: 'OpenRouter', // OpenRouter

    // Image/Video generation
    2: 'Midjourney', // MjProxy
    5: 'Midjourney', // MjProxyPlus
    50: 'Kling', // Kling
    51: 'Jimeng', // Jimeng
    52: 'Vidu', // Vidu
    36: 'Suno', // SunoAPI
    55: 'OpenAI', // Sora
    54: 'Doubao', // Doubao
    56: 'Replicate', // Replicate

    // Tools & Platforms
    37: 'Dify', // Dify
    38: 'Jina', // Jina
    22: 'FastGPT', // FastGPT
    47: 'Xinference', // Xinference
    53: 'OpenAI', // Submodel

    // AI Proxy services
    10: 'OpenAI', // AI Proxy
    21: 'OpenAI', // AI Proxy Library
    12: 'OpenAI', // API2GPT
    13: 'OpenAI', // AIGC2D
    9: 'OpenAI', // AILS
  }

  return TYPE_TO_ICON[type] || 'OpenAI'
}

// ============================================================================
// Status Utilities
// ============================================================================

/**
 * Get status badge configuration
 */
export function getChannelStatusBadge(status: number) {
  return (
    CHANNEL_STATUS_CONFIG[status as keyof typeof CHANNEL_STATUS_CONFIG] ||
    CHANNEL_STATUS_CONFIG[0]
  )
}

/**
 * Get multi-key status badge configuration
 */
export function getMultiKeyStatusBadge(status: number) {
  return (
    MULTI_KEY_STATUS_CONFIG[status as keyof typeof MULTI_KEY_STATUS_CONFIG] ||
    MULTI_KEY_STATUS_CONFIG[1]
  )
}

/**
 * Check if channel is enabled
 */
export function isChannelEnabled(channel: Channel): boolean {
  return channel.status === 1
}

/**
 * Check if channel is multi-key
 */
export function isMultiKeyChannel(channel: Channel): boolean {
  return channel.channel_info?.is_multi_key || false
}

// ============================================================================
// Key Formatting
// ============================================================================

/**
 * Format channel key for display
 * Masks the key for security, showing only first and last few characters
 */
export function formatChannelKey(
  key: string,
  isMultiKey: boolean = false
): string {
  if (!key) {
    return ''
  }

  if (isMultiKey) {
    const keys = key.split('\n').filter((k) => k.trim())
    return `${keys.length} keys`
  }

  if (key.length <= 16) {
    // For short keys, mask middle part
    return `${key.slice(0, 4)}...${key.slice(-4)}`
  }

  // For longer keys, show more context
  return `${key.slice(0, 8)}...${key.slice(-8)}`
}

/**
 * Format key preview for multi-key display
 */
export function formatKeyPreview(key: string, maxLength: number = 10): string {
  if (!key) {
    return ''
  }
  if (key.length <= maxLength) {
    return key
  }
  return `${key.slice(0, maxLength)}...`
}

/**
 * Count keys in multi-key string
 */
export function countKeys(key: string): number {
  if (!key) {
    return 0
  }
  return key.split('\n').filter((k) => k.trim()).length
}

// ============================================================================
// Model & Group Parsing
// ============================================================================

/**
 * Parse comma-separated models list
 */
export function parseModelsList(models: string): string[] {
  if (!models) {
    return []
  }
  return models
    .split(',')
    .map((m) => m.trim())
    .filter((m) => m.length > 0)
}

/**
 * Parse comma-separated groups list.
 * Sorts with 'default' group first, then locale-sorted alphabetically.
 */
export function parseGroupsList(groups: string): string[] {
  if (!groups) {
    return []
  }
  const list = groups
    .split(',')
    .map((g) => g.trim())
    .filter((g) => g.length > 0)
  return list.sort((a, b) => {
    if (a === 'default') {
      return -1
    }
    if (b === 'default') {
      return 1
    }
    return a.localeCompare(b)
  })
}

/**
 * Format models array back to string
 */
export function formatModelsString(models: string[]): string {
  return models.join(',')
}

/**
 * Format groups array back to string
 */
export function formatGroupsString(groups: string[]): string {
  return groups.join(',')
}

// ============================================================================
// Settings Parsing
// ============================================================================

/**
 * Parse channel settings JSON
 */
export function parseChannelSettings(
  settingStr: string | null | undefined
): ChannelSettings {
  if (!settingStr) {
    return {}
  }
  try {
    return JSON.parse(settingStr) as ChannelSettings
  } catch {
    return {}
  }
}

/**
 * Parse channel other settings JSON
 */
export function parseChannelOtherSettings(
  settingsStr: string | null | undefined
): ChannelOtherSettings {
  if (!settingsStr || settingsStr === '{}') {
    return {}
  }
  try {
    return JSON.parse(settingsStr) as ChannelOtherSettings
  } catch {
    return {}
  }
}

/**
 * Validate JSON string
 */
export function validateChannelSettings(settings: string): boolean {
  if (!settings || settings.trim() === '') {
    return true
  }
  try {
    JSON.parse(settings)
    return true
  } catch {
    return false
  }
}

// ============================================================================
// Balance Formatting
// ============================================================================

/**
 * Format balance with currency symbol
 */
export function formatBalance(balance: number | null | undefined): string {
  if (balance == null || Number.isNaN(balance)) {
    return '-'
  }
  return formatCurrencyFromUSD(balance, {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: false,
  })
}

/**
 * Get balance status color
 */
export function getBalanceVariant(
  balance: number
): 'success' | 'warning' | 'danger' | 'neutral' {
  if (balance === 0) {
    return 'neutral'
  }
  if (balance < 1) {
    return 'danger'
  }
  if (balance < 10) {
    return 'warning'
  }
  return 'success'
}

// ============================================================================
// Response Time Utilities
// ============================================================================

/** Optional i18n: (key, options) => string, e.g. useTranslation().t */
type TFunction = (key: string, options?: { value?: number | string }) => string

/**
 * Format response time in milliseconds to human-readable.
 * Pass `t` from useTranslation() for i18n (e.g. "Not tested", "{{value}}ms", "{{value}}s").
 */
export function formatResponseTime(timeMs: number, t?: TFunction): string {
  if (timeMs === 0) {
    return t ? t('Not tested') : 'Not tested'
  }
  if (timeMs < 1000) {
    return t ? t('{{value}}ms', { value: timeMs }) : `${timeMs}ms`
  }
  return t
    ? t('{{value}}s', { value: (timeMs / 1000).toFixed(2) })
    : `${(timeMs / 1000).toFixed(2)}s`
}

/**
 * Get response time performance rating
 */
export function getResponseTimeConfig(timeMs: number) {
  if (timeMs === 0) {
    return RESPONSE_TIME_CONFIG.UNKNOWN
  }
  if (timeMs <= RESPONSE_TIME_THRESHOLDS.EXCELLENT) {
    return RESPONSE_TIME_CONFIG.EXCELLENT
  }
  if (timeMs <= RESPONSE_TIME_THRESHOLDS.GOOD) {
    return RESPONSE_TIME_CONFIG.GOOD
  }
  if (timeMs <= RESPONSE_TIME_THRESHOLDS.FAIR) {
    return RESPONSE_TIME_CONFIG.FAIR
  }
  if (timeMs <= RESPONSE_TIME_THRESHOLDS.POOR) {
    return RESPONSE_TIME_CONFIG.POOR
  }
  return RESPONSE_TIME_CONFIG.POOR
}

// ============================================================================
// Time Formatting
// ============================================================================

/**
 * Format a Unix timestamp (seconds) as a compact, locale-aware relative time.
 * Uses `Intl.RelativeTimeFormat` with the `narrow` style so the label stays
 * short inside table cells, e.g. "4h ago" / "42m ago" (en) or "4 小时前" (zh),
 * instead of the verbose "4 hours ago".
 */
export function formatRelativeTime(
  timestamp: number,
  locale?: Intl.LocalesArgument
): string {
  if (!timestamp || timestamp === 0) {
    return 'Never'
  }

  try {
    const diffSec = timestamp - Date.now() / 1000
    const absSec = Math.abs(diffSec)
    const rtf = new Intl.RelativeTimeFormat(locale, {
      numeric: 'always',
      style: 'narrow',
    })

    const MINUTE = 60
    const HOUR = 60 * MINUTE
    const DAY = 24 * HOUR
    const MONTH = 30 * DAY
    const YEAR = 365 * DAY

    let value: number
    let unit: Intl.RelativeTimeFormatUnit
    if (absSec < MINUTE) {
      value = Math.round(diffSec)
      unit = 'second'
    } else if (absSec < HOUR) {
      value = Math.round(diffSec / MINUTE)
      unit = 'minute'
    } else if (absSec < DAY) {
      value = Math.round(diffSec / HOUR)
      unit = 'hour'
    } else if (absSec < MONTH) {
      value = Math.round(diffSec / DAY)
      unit = 'day'
    } else if (absSec < YEAR) {
      value = Math.round(diffSec / MONTH)
      unit = 'month'
    } else {
      value = Math.round(diffSec / YEAR)
      unit = 'year'
    }

    const formatted = rtf.format(value, unit)
    const primaryLocale = Array.isArray(locale) ? locale[0] : locale
    const language = primaryLocale?.toString()
    if (language?.startsWith('zh')) {
      return formatted.replaceAll(/(\d)([\u4e00-\u9fff])/g, '$1 $2')
    }
    return formatted
  } catch {
    return 'Unknown'
  }
}

/**
 * Format Unix timestamp to date string
 */
export function formatTimestamp(timestamp: number): string {
  if (!timestamp || timestamp === 0) {
    return 'N/A'
  }

  try {
    return formatTimestampToDate(timestamp)
  } catch {
    return 'Invalid date'
  }
}

// ============================================================================
// Quota Formatting
// ============================================================================

/** Format quota units using the global currency display configuration. */
export function formatQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: true,
  })
}

// ============================================================================
// Priority & Weight Utilities
// ============================================================================

/**
 * Get priority display value
 */
export function getPriorityDisplay(
  priority: number | null | undefined
): string {
  if (priority === null || priority === undefined) {
    return '0'
  }
  return String(priority)
}

/**
 * Get weight display value
 */
export function getWeightDisplay(weight: number | null | undefined): string {
  if (weight === null || weight === undefined) {
    return '0'
  }
  return String(weight)
}

// ============================================================================
// Validation Utilities
// ============================================================================

/**
 * Validate channel name
 */
export function validateChannelName(name: string): boolean {
  return name.trim().length > 0
}

/**
 * Validate API key format
 */
export function validateApiKey(key: string): boolean {
  return key.trim().length > 0
}

/**
 * Validate models list
 */
export function validateModels(models: string): boolean {
  return parseModelsList(models).length > 0
}

/**
 * Validate groups list
 */
export function validateGroups(groups: string): boolean {
  return parseGroupsList(groups).length > 0
}

/**
 * Check if channel needs attention (low balance, auto-disabled, etc.)
 */
export function channelNeedsAttention(channel: Channel): boolean {
  // Auto-disabled
  if (channel.status === 3) {
    return true
  }

  // Low balance (less than $1)
  if (channel.balance > 0 && channel.balance < 1) {
    return true
  }

  // Multi-key channel with all keys disabled
  if (
    channel.channel_info?.is_multi_key &&
    channel.channel_info.multi_key_status_list &&
    Object.keys(channel.channel_info.multi_key_status_list).length >=
      channel.channel_info.multi_key_size
  ) {
    return true
  }

  return false
}

/**
 * Get attention reason for channel
 */
export function getAttentionReason(channel: Channel): string | null {
  if (channel.status === 3) {
    return 'Auto-disabled'
  }
  if (channel.balance > 0 && channel.balance < 1) {
    return 'Low balance'
  }
  if (
    channel.channel_info?.is_multi_key &&
    channel.channel_info.multi_key_status_list &&
    Object.keys(channel.channel_info.multi_key_status_list).length >=
      channel.channel_info.multi_key_size
  ) {
    return 'All keys disabled'
  }
  return null
}

// ============================================================================
// Tag Aggregation Utilities
// ============================================================================

/**
 * Tag row type (extends Channel with children)
 */
export type TagRow = Channel & {
  children: Channel[]
}

/**
 * Type guard to check whether a row is a tag aggregate row
 */
export function isTagAggregateRow(row: Channel | TagRow): row is TagRow {
  return Array.isArray((row as TagRow).children)
}

export function getChannelTableRowId(row: Channel | TagRow): string {
  if (isTagAggregateRow(row)) {
    return `tag:${row.tag || ''}`
  }

  return `channel:${row.id}`
}

/**
 * Aggregate channels by tag for tag mode display
 * Converts flat array into tree structure grouped by tag
 */
export function aggregateChannelsByTag(
  channels: Channel[]
): (Channel | TagRow)[] {
  const tagMap = new Map<string, TagRow>()
  const result: (Channel | TagRow)[] = []

  for (const channel of channels) {
    const tag = channel.tag || ''

    if (!tagMap.has(tag)) {
      // Create tag aggregate row
      const tagRow = {
        ...channel,
        key: tag,
        id: tag as unknown as number,
        tag,
        name: tag,
        type: 0,
        status: undefined as unknown as number,
        group: '',
        used_quota: 0,
        response_time: 0,
        priority: -1 as unknown as number | null,
        weight: -1 as unknown as number | null,
        balance: 0,
        test_time: 0,
        created_time: 0,
        balance_updated_time: 0,
        models: '',
        children: [],
      } as TagRow
      tagMap.set(tag, tagRow)
      result.push(tagRow)
    }

    const tagRow = tagMap.get(tag)
    if (!tagRow) {
      continue
    }

    // Add to children
    tagRow.children.push(channel)
    const childCount = tagRow.children.length

    // Aggregate used_quota (sum)
    tagRow.used_quota += channel.used_quota

    // Aggregate response_time (average)
    tagRow.response_time =
      (tagRow.response_time * (childCount - 1) + channel.response_time) /
      childCount

    // Aggregate priority (same value or null if different)
    if (tagRow.priority === -1) {
      tagRow.priority = channel.priority
    } else if (tagRow.priority !== channel.priority) {
      tagRow.priority = null
    }

    // Aggregate weight (same value or null if different)
    if (tagRow.weight === -1) {
      tagRow.weight = channel.weight
    } else if (tagRow.weight !== channel.weight) {
      tagRow.weight = null
    }

    // Aggregate group (concatenate and deduplicate)
    if (tagRow.group === '') {
      tagRow.group = channel.group
    } else {
      const existingGroups = new Set(tagRow.group.split(',').filter(Boolean))
      const newGroups = channel.group.split(',').filter(Boolean)
      newGroups.forEach((g) => {
        if (!existingGroups.has(g)) {
          tagRow.group += `,${g}`
        }
      })
    }

    // Aggregate status (enabled if any child is enabled)
    if (channel.status === 1) {
      tagRow.status = 1
    } else if (tagRow.status === undefined) {
      tagRow.status = channel.status
    }
  }

  return result
}

// ============================================================================
// Key Management Utilities
// ============================================================================

/**
 * Deduplicate keys from a multiline string
 * @param keysText - Text with one key per line
 * @returns Object with deduplicated keys and statistics
 */
export function deduplicateKeys(keysText: string): {
  deduplicatedText: string
  beforeCount: number
  afterCount: number
  removedCount: number
} {
  if (!keysText || keysText.trim() === '') {
    return {
      deduplicatedText: '',
      beforeCount: 0,
      afterCount: 0,
      removedCount: 0,
    }
  }

  // Split by lines
  const keyLines = keysText.split('\n')
  const beforeCount = keyLines.length

  // Use Set for deduplication, maintaining order
  const keySet = new Set<string>()
  const deduplicatedKeys: string[] = []

  keyLines.forEach((line) => {
    const trimmedLine = line.trim()
    if (trimmedLine && !keySet.has(trimmedLine)) {
      keySet.add(trimmedLine)
      deduplicatedKeys.push(trimmedLine)
    }
  })

  const afterCount = deduplicatedKeys.length
  const deduplicatedText = deduplicatedKeys.join('\n')

  return {
    deduplicatedText,
    beforeCount,
    afterCount,
    removedCount: beforeCount - afterCount,
  }
}

/**
 * Get key prompt based on channel type
 */
export function getKeyPromptForType(type: number): string {
  return TYPE_TO_KEY_PROMPT[type] || 'Enter API key for this channel'
}
