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
import dayjs from '@/lib/dayjs'

import {
  formatCurrencyFromUSD,
  formatQuotaWithCurrency,
  getCurrencyDisplay,
  getCurrencyFractionDigits,
} from './currency'

// ============================================================================
// Number Formatting
// ============================================================================

export function formatNumber(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  if (value == null || Number.isNaN(value as number)) return '-'
  return Intl.NumberFormat(locales, { maximumFractionDigits: 2 }).format(
    value as number
  )
}

export function formatCompactNumber(
  value: number | null | undefined,
  locales?: Intl.LocalesArgument
): string {
  if (value == null || Number.isNaN(value as number)) return '-'
  return Intl.NumberFormat(locales, {
    notation: 'compact',
    maximumFractionDigits: 1,
  }).format(value as number)
}

export function formatPercent(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value as number)) return '-'
  return Intl.NumberFormat(undefined, {
    style: 'percent',
    maximumFractionDigits: 2,
  }).format((value as number) / 100)
}

export function formatCurrencyUSD(value: number | null | undefined): string {
  return formatCurrencyFromUSD(value == null ? null : (value as number))
}

// ============================================================================
// Quota Formatting (500,000 units = $1)
// ============================================================================

/**
 * Format quota into the configured display amount.
 * Quota is stored in units where `quotaPerUnit` equals 1 USD.
 */
export function formatQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 2,
    digitsSmall: 4,
    abbreviate: true,
  })
}

/**
 * Parse quota from the current display input back to quota units.
 */
export function parseQuotaFromDollars(amount: number): number {
  if (!Number.isFinite(amount)) return 0

  const { config, meta } = getCurrencyDisplay()

  // Tokens-only or raw quota mode
  if (meta.kind === 'tokens') {
    return Math.round(amount)
  }

  const exchangeRate =
    meta.kind === 'currency' || meta.kind === 'custom' ? meta.exchangeRate : 1

  const usdAmount = exchangeRate > 0 ? amount / exchangeRate : amount

  return Math.round(usdAmount * config.quotaPerUnit)
}

/**
 * Convert quota units to the configured display amount.
 * Reverse of parseQuotaFromDollars.
 */
export function quotaUnitsToDollars(units: number): number {
  const { config, meta } = getCurrencyDisplay()
  return quotaUnitsToDisplayAmount(units, config.quotaPerUnit, meta)
}

function quotaUnitsToDisplayAmount(
  units: number,
  quotaPerUnit: number,
  meta: ReturnType<typeof getCurrencyDisplay>['meta']
): number {
  if (meta.kind === 'tokens') {
    return units
  }

  return (units / quotaPerUnit) * meta.exchangeRate
}

/**
 * Convert quota units to a plain number suitable for an editable input.
 * Uses the same precision as quota list formatting without symbols or suffixes.
 */
export function quotaUnitsToEditableAmount(units: number): number {
  const { config, meta } = getCurrencyDisplay()
  const amount = quotaUnitsToDisplayAmount(units, config.quotaPerUnit, meta)

  if (meta.kind === 'tokens') {
    return Math.round(amount)
  }

  return Number(amount.toFixed(getCurrencyFractionDigits(amount)))
}

/** Return the input step matching the configured editable quota precision. */
export function getEditableQuotaStep(): number {
  const { meta } = getCurrencyDisplay()
  if (meta.kind === 'tokens') {
    return 1
  }

  return 10 ** -getCurrencyFractionDigits(0)
}

// ============================================================================
// Timestamp Formatting
// ============================================================================

/**
 * Format Unix timestamp (seconds) to YYYY-MM-DD HH:mm:ss
 */
export function formatTimestamp(timestamp: number): string {
  if (timestamp === -1) {
    return 'Never'
  }
  return formatTimestampToDate(timestamp)
}

/**
 * Format timestamp to YYYY-MM-DD HH:mm:ss
 * @param timestamp - Timestamp in seconds or milliseconds
 * @param unit - Unit of the timestamp ('seconds' or 'milliseconds')
 */
export function formatTimestampToDate(
  timestamp?: number,
  unit: 'seconds' | 'milliseconds' = 'seconds'
): string {
  if (!timestamp || timestamp === -1 || timestamp === 0) {
    return '-'
  }
  const ms = unit === 'seconds' ? timestamp * 1000 : timestamp
  return dayjs(ms).format('YYYY-MM-DD HH:mm:ss')
}

/**
 * Format timestamp as relative time, e.g. "30 seconds ago".
 * @param timestamp - Timestamp in seconds or milliseconds
 * @param unit - Unit of the timestamp ('seconds' or 'milliseconds')
 * @param locales - Locale passed to Intl.RelativeTimeFormat
 */
export function formatTimestampRelative(
  timestamp?: number,
  unit: 'seconds' | 'milliseconds' = 'seconds',
  locales?: Intl.LocalesArgument
): string {
  if (!timestamp || timestamp === -1 || timestamp === 0) {
    return '-'
  }

  const ms = unit === 'seconds' ? timestamp * 1000 : timestamp
  const diffSeconds = Math.round((ms - Date.now()) / 1000)
  const absSeconds = Math.abs(diffSeconds)
  const formatter = new Intl.RelativeTimeFormat(locales, {
    numeric: 'always',
  })

  if (absSeconds < 60) {
    return formatter.format(diffSeconds, 'second')
  }
  if (absSeconds < 3600) {
    return formatter.format(Math.round(diffSeconds / 60), 'minute')
  }
  if (absSeconds < 86400) {
    return formatter.format(Math.round(diffSeconds / 3600), 'hour')
  }
  if (absSeconds < 2592000) {
    return formatter.format(Math.round(diffSeconds / 86400), 'day')
  }
  if (absSeconds < 31536000) {
    return formatter.format(Math.round(diffSeconds / 2592000), 'month')
  }
  return formatter.format(Math.round(diffSeconds / 31536000), 'year')
}

/** Format a Date object to YYYY-MM-DD HH:mm:ss */
export function formatDateTimeStr(date: Date): string {
  return dayjs(date).format('YYYY-MM-DD HH:mm:ss')
}

/** Format a Date object to YYYY-MM-DD */
export function formatDateStr(date: Date): string {
  return dayjs(date).format('YYYY-MM-DD')
}

/** Format a Date object to HH:mm:ss */
export function formatTimeStr(date: Date): string {
  return dayjs(date).format('HH:mm:ss')
}

/**
 * Format quota for usage logs with higher precision
 * Uses 6 decimal places to show very small costs accurately
 */
export function formatLogQuota(quota: number): string {
  return formatQuotaWithCurrency(quota, {
    digitsLarge: 4,
    digitsSmall: 6,
    abbreviate: false,
  })
}

/**
 * Format tokens count with K/M suffixes
 */
export function formatTokens(tokens: number): string {
  if (tokens === 0) return '-'
  if (tokens < 1000) return tokens.toString()
  if (tokens < 1000000) return `${(tokens / 1000).toFixed(1)}K`
  return `${(tokens / 1000000).toFixed(2)}M`
}

/**
 * Format use time in seconds with appropriate unit
 */
export function formatUseTime(seconds: number): string {
  if (seconds < 60) return `${seconds.toFixed(1)}s`
  const minutes = Math.floor(seconds / 60)
  const remainingSeconds = seconds % 60
  return `${minutes}m ${remainingSeconds.toFixed(0)}s`
}

/**
 * Format timestamp to date input value (YYYY-MM-DDTHH:mm)
 */
export function formatTimestampForInput(timestamp: number): string {
  if (timestamp === -1) {
    return ''
  }
  return dayjs(timestamp * 1000).format('YYYY-MM-DDTHH:mm')
}

/**
 * Parse datetime-local input to Unix timestamp
 */
export function parseTimestampFromInput(value: string): number {
  if (!value) {
    return -1
  }
  const date = new Date(value)
  return Math.floor(date.getTime() / 1000)
}

// ============================================================================
// Color Generation
// ============================================================================

/**
 * Generate a consistent color from a string
 * Uses HSL for better color distribution
 */
export function stringToColor(str: string): string {
  if (!str) return 'gray'

  // Generate hash from string
  let hash = 0
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash)
    hash = hash & hash // Convert to 32-bit integer
  }

  // Use hash to generate hue (0-360)
  const hue = Math.abs(hash % 360)

  // Use saturation and lightness that work well for tags
  const saturation = 65 + (Math.abs(hash) % 10) // 65-75%
  const lightness = 55 + (Math.abs(hash >> 8) % 10) // 55-65%

  return `hsl(${hue}, ${saturation}%, ${lightness}%)`
}
