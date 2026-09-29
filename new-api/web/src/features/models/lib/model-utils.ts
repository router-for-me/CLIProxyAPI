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

import { formatTimestampToDate } from '@/lib/format'

import { getNameRuleConfig, getQuotaTypeConfig } from '../constants'
import type { NameRule, Model } from '../types'

// ============================================================================
// Time Formatting
// ============================================================================

/**
 * Format timestamp to standard date string (YYYY-MM-DD HH:mm:ss)
 */
export function formatTimestamp(timestamp: number): string {
  if (!timestamp || timestamp === 0) return '-'
  return formatTimestampToDate(timestamp)
}

/**
 * Format relative time
 */
export function formatRelativeTime(timestamp: number): string {
  if (!timestamp || timestamp === 0) return 'Never'

  const now = Date.now()
  const time = timestamp * 1000
  const diff = now - time

  const seconds = Math.floor(diff / 1000)
  const minutes = Math.floor(seconds / 60)
  const hours = Math.floor(minutes / 60)
  const days = Math.floor(hours / 24)

  if (days > 0) return `${days} day${days > 1 ? 's' : ''} ago`
  if (hours > 0) return `${hours} hour${hours > 1 ? 's' : ''} ago`
  if (minutes > 0) return `${minutes} minute${minutes > 1 ? 's' : ''} ago`
  return `${seconds} second${seconds !== 1 ? 's' : ''} ago`
}

// ============================================================================
// Tags Parsing
// ============================================================================

/**
 * Parse tags string to array
 */
export function parseModelTags(tags: string | undefined): string[] {
  if (!tags) return []
  return tags
    .split(',')
    .map((tag) => tag.trim())
    .filter(Boolean)
}

/**
 * Format tags array to string
 */
export function formatTagsString(tags: string[]): string {
  return tags.join(',')
}

// ============================================================================
// Endpoints Parsing
// ============================================================================

/**
 * Parse endpoints JSON string
 */
export function parseEndpoints(
  endpoints: string | undefined
): Record<string, unknown> | unknown[] | null {
  if (!endpoints || endpoints.trim() === '') return null

  try {
    return JSON.parse(endpoints)
  } catch {
    return null
  }
}

/**
 * Format endpoints to display
 */
export function formatEndpointsDisplay(
  endpoints: string | undefined
): string[] {
  const parsed = parseEndpoints(endpoints)
  if (!parsed) return []

  if (typeof parsed === 'object' && !Array.isArray(parsed)) {
    return Object.keys(parsed)
  }

  if (Array.isArray(parsed)) {
    return parsed.map(String)
  }

  return []
}

// ============================================================================
// Name Rule Utils
// ============================================================================

/**
 * Get name rule label
 */
export function getNameRuleLabelByRule(rule: NameRule, t: TFunction): string {
  const config = getNameRuleConfig(t)
  return config[rule]?.label || '-'
}

/**
 * Get name rule config by rule
 */
export function getNameRuleConfigByRule(rule: NameRule, t: TFunction) {
  const config = getNameRuleConfig(t)
  return config[rule] || config[0]
}

// ============================================================================
// Quota Type Utils
// ============================================================================

/**
 * Format quota types array
 */
export function formatQuotaTypes(
  quotaTypes: number[] | undefined,
  t: TFunction
): string {
  if (!quotaTypes || quotaTypes.length === 0) return '-'
  const config = getQuotaTypeConfig(t)
  return quotaTypes.map((qt) => config[qt]?.label || String(qt)).join(', ')
}

// ============================================================================
// Model Validation
// ============================================================================

/**
 * Validate model name
 */
export function validateModelName(name: string): boolean {
  return name.trim().length > 0
}

/**
 * Validate endpoints JSON
 */
export function validateEndpointsJSON(endpoints: string): boolean {
  if (!endpoints || endpoints.trim() === '') return true

  try {
    JSON.parse(endpoints)
    return true
  } catch {
    return false
  }
}

// ============================================================================
// Model Status Utils
// ============================================================================

/**
 * Check if model is enabled
 */
export function isModelEnabled(model: Model): boolean {
  return model.status === 1
}

/**
 * Check if model syncs with official
 */
export function isModelSyncOfficial(model: Model): boolean {
  return model.sync_official === 1
}

// Keep table labels compact; the drawer and tooltip share the full explanation.
export function getModelChannelState(model: Model) {
  const available = model.bound_channels?.length ?? 0
  const configured = model.configured_channel_count ?? available
  if (configured === 0) {
    if (model.name_rule !== 0) {
      return {
        label: 'No matching channels',
        description: 'No configured channel models match this metadata rule.',
      }
    }
    return {
      label: 'Metadata only',
      description:
        'No channel is configured. This model will not appear in the model square.',
    }
  }
  if (available === 0) {
    return {
      label: 'No available channels',
      description:
        'No channel is currently available. This model will not appear in the model square.',
    }
  }
  return {
    label: 'Available channels: {{count}}',
    description:
      'Listing also depends on metadata visibility and the user’s group access.',
  }
}
