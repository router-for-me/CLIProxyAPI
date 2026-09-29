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
/**
 * ============================================================================
 * Currency Formatting Library
 * ============================================================================
 *
 * This module provides currency formatting utilities that handle the conversion
 * between system USD amounts, local currency, and token displays based on
 * admin-configured settings.
 *
 * ## Key Concepts
 *
 * 1. **System USD**: Internal currency unit used throughout the system (e.g., 10 USD)
 * 2. **Local Currency**: Admin-configured display currency (e.g., CNY, custom currency)
 * 3. **Exchange Rate (usdExchangeRate)**: Conversion rate from USD to local currency
 *    - Example: usdExchangeRate = 7 means 1 USD = 7 CNY
 * 4. **Recharge Price (priceRatio)**: Cost in local currency to purchase 1 system USD
 *    - Example: priceRatio = 5 means user pays 5 CNY to get 1 USD credit
 * 5. **Tokens**: Alternative display unit (e.g., 500,000 tokens = 1 USD)
 *
 * ## When to Use Each Function
 *
 * - `formatCurrencyFromUSD()`: Use for quota/balance display (stored as USD, converted for display)
 * - `formatBillingCurrencyFromUSD()`: Use for billing/pricing displays (never shows tokens)
 * - `formatLocalCurrencyAmount()`: Use for payment amounts already in local currency
 * - `formatQuotaWithCurrency()`: Use for raw quota values (converts to USD first)
 *
 * ## Example Scenario
 *
 * Admin Configuration:
 * - quotaDisplayType: 'CNY'
 * - usdExchangeRate: 7 (1 USD = 7 CNY)
 * - priceRatio: 5 (5 CNY per 1 USD credit)
 * - quotaPerUnit: 500000 (tokens per USD)
 *
 * User Flow:
 * 1. Recharge option: 10 USD
 *    - Display: formatCurrencyFromUSD(10) → "¥70"
 * 2. Payment amount: 10 × 5 = 50 (already in CNY)
 *    - Display: formatLocalCurrencyAmount(50) → "¥50"
 * 3. User receives: 10 USD credit
 *    - Balance display: formatCurrencyFromUSD(10) → "¥70"
 *
 * ## Quick Reference Guide
 *
 * | Scenario | Input Type | Function to Use | Why |
 * |----------|-----------|-----------------|-----|
 * | User balance display | USD (from DB) | `formatCurrencyFromUSD()` | Needs conversion to display currency |
 * | Recharge option button | USD | `formatCurrencyFromUSD()` | Needs conversion to local currency |
 * | Payment confirmation | Already local currency | `formatLocalCurrencyAmount()` | Already converted via priceRatio |
 * | Billing history Amount | USD (from DB) | `formatCurrencyFromUSD()` | Historical USD needs conversion |
 * | Billing history Payment | Local currency | `formatNumber()` | Just show number, no symbol |
 * | Model pricing | USD | `formatBillingCurrencyFromUSD()` | Never show as tokens |
 * | Raw quota from API | Tokens | `formatQuotaWithCurrency()` | Convert tokens → USD → display |
 *
 * ## Critical Rules
 *
 * 1. **Never double-convert**: If you multiply by exchangeRate, use formatLocalCurrencyAmount()
 * 2. **Database USD values**: Always use formatCurrencyFromUSD() for amounts stored as USD
 * 3. **Payment amounts**: Always use formatLocalCurrencyAmount() for priceRatio-calculated values
 * 4. **Billing displays**: Use formatBillingCurrencyFromUSD() to avoid token display
 * 5. **Effective exchange rate**: When quotaDisplayType is 'USD', use rate of 1 regardless of config
 */
import {
  useSystemConfigStore,
  DEFAULT_CURRENCY_CONFIG,
  type CurrencyConfig,
  type CurrencyDisplayType,
} from '@/stores/system-config-store'

export interface CurrencyFormatOptions {
  /** Fraction digits to use when |value| >= 1 */
  digitsLarge?: number
  /** Fraction digits to use when |value| < 1 */
  digitsSmall?: number
  /** Whether to abbreviate thousands with k suffix */
  abbreviate?: boolean
  /** Minimal absolute value to display when rounding would produce zero */
  minimumNonZero?: number
  /**
   * Use locale-aware compact notation for large values (e.g. "$28万" in zh,
   * "$280K" in en). The currency symbol is preserved.
   */
  compact?: boolean
  /** Whether to include the currency/custom symbol. Token displays are unchanged. */
  showSymbol?: boolean
  /** Locale used for number formatting (defaults to the runtime locale) */
  locale?: Intl.LocalesArgument | undefined
}

type ResolvedCurrencyFormatOptions = Omit<
  Required<CurrencyFormatOptions>,
  'locale'
> & {
  locale: Intl.LocalesArgument | undefined
}

type DisplayMeta =
  | {
      kind: 'currency'
      symbol: string
      currencyCode: string
      exchangeRate: number
    }
  | {
      kind: 'custom'
      symbol: string
      exchangeRate: number
    }
  | {
      kind: 'tokens'
      /** Number of tokens per USD */
      quotaPerUnit: number
    }

const DEFAULT_FORMAT_OPTIONS: ResolvedCurrencyFormatOptions = {
  digitsLarge: 2,
  digitsSmall: 4,
  abbreviate: true,
  minimumNonZero: 0,
  compact: false,
  showSymbol: true,
  locale: undefined,
}

const DISPLAY_TYPE_VALUES = ['USD', 'CNY', 'TOKENS', 'CUSTOM'] as const
type DisplayTypeLiteral = (typeof DISPLAY_TYPE_VALUES)[number]

export function isCurrencyDisplayType(
  value: unknown
): value is CurrencyDisplayType {
  return (
    typeof value === 'string' &&
    DISPLAY_TYPE_VALUES.includes(value as DisplayTypeLiteral)
  )
}

export function parseCurrencyDisplayType(
  value: unknown,
  fallback: CurrencyDisplayType = 'USD'
): CurrencyDisplayType {
  return isCurrencyDisplayType(value) ? value : fallback
}

function getConfig(): CurrencyConfig {
  const { config } = useSystemConfigStore.getState()
  const currency = config?.currency ?? DEFAULT_CURRENCY_CONFIG
  return {
    ...DEFAULT_CURRENCY_CONFIG,
    ...currency,
    quotaPerUnit:
      currency?.quotaPerUnit && currency.quotaPerUnit > 0
        ? currency.quotaPerUnit
        : DEFAULT_CURRENCY_CONFIG.quotaPerUnit,
    usdExchangeRate:
      currency?.usdExchangeRate && currency.usdExchangeRate > 0
        ? currency.usdExchangeRate
        : DEFAULT_CURRENCY_CONFIG.usdExchangeRate,
    customCurrencyExchangeRate:
      currency?.customCurrencyExchangeRate &&
      currency.customCurrencyExchangeRate > 0
        ? currency.customCurrencyExchangeRate
        : DEFAULT_CURRENCY_CONFIG.customCurrencyExchangeRate,
    customCurrencySymbol:
      currency?.customCurrencySymbol?.trim() ||
      DEFAULT_CURRENCY_CONFIG.customCurrencySymbol,
  }
}

function getDisplayMeta(config: CurrencyConfig): DisplayMeta {
  switch (config.quotaDisplayType) {
    case 'CNY':
      return {
        kind: 'currency',
        symbol: '¥',
        currencyCode: 'CNY',
        exchangeRate: config.usdExchangeRate,
      }
    case 'CUSTOM':
      return {
        kind: 'custom',
        symbol: config.customCurrencySymbol,
        exchangeRate: config.customCurrencyExchangeRate,
      }
    case 'TOKENS':
      return {
        kind: 'tokens',
        quotaPerUnit: config.quotaPerUnit,
      }
    case 'USD':
    default:
      return {
        kind: 'currency',
        symbol: '$',
        currencyCode: 'USD',
        exchangeRate: 1,
      }
  }
}

function getBillingDisplayMeta(config: CurrencyConfig): DisplayMeta {
  const meta = getDisplayMeta(config)
  if (meta.kind === 'tokens') {
    return {
      kind: 'currency',
      symbol: '$',
      currencyCode: 'USD',
      exchangeRate: 1,
    }
  }
  return meta
}

function mergeOptions(
  options?: CurrencyFormatOptions
): ResolvedCurrencyFormatOptions {
  if (!options) return DEFAULT_FORMAT_OPTIONS
  return {
    digitsLarge: options.digitsLarge ?? DEFAULT_FORMAT_OPTIONS.digitsLarge,
    digitsSmall: options.digitsSmall ?? DEFAULT_FORMAT_OPTIONS.digitsSmall,
    abbreviate: options.abbreviate ?? DEFAULT_FORMAT_OPTIONS.abbreviate,
    minimumNonZero:
      options.minimumNonZero ?? DEFAULT_FORMAT_OPTIONS.minimumNonZero,
    compact: options.compact ?? DEFAULT_FORMAT_OPTIONS.compact,
    showSymbol: options.showSymbol ?? DEFAULT_FORMAT_OPTIONS.showSymbol,
    locale: options.locale ?? DEFAULT_FORMAT_OPTIONS.locale,
  }
}

function getFractionDigits(
  value: number,
  digitsLarge: number,
  digitsSmall: number
): number {
  return Math.abs(value) >= 1 ? digitsLarge : digitsSmall
}

/** Return the configured fraction digits for a plain currency value. */
export function getCurrencyFractionDigits(
  value: number,
  options?: CurrencyFormatOptions
): number {
  const merged = mergeOptions(options)
  return getFractionDigits(value, merged.digitsLarge, merged.digitsSmall)
}

function removeTrailingZeros(str: string): string {
  if (!str.includes('.')) return str
  return str.replace(/(\.[0-9]*?)0+$/, '$1').replace(/\.$/, '')
}

function formatNumberWithSuffix(
  value: number,
  digitsLarge: number,
  digitsSmall: number,
  abbreviate: boolean
): string {
  const abs = Math.abs(value)
  if (abbreviate && abs >= 1000) {
    const result = value / 1000
    return `${removeTrailingZeros(result.toFixed(1))}k`
  }

  const digits = getFractionDigits(value, digitsLarge, digitsSmall)
  return removeTrailingZeros(value.toFixed(digits))
}

function adjustForMinimum(
  value: number,
  digits: number,
  minimumNonZero: number
): number {
  if (value === 0) return value

  const threshold = minimumNonZero > 0 ? minimumNonZero : Math.pow(10, -digits)
  const abs = Math.abs(value)
  if (abs > 0 && abs < threshold) {
    return value > 0 ? threshold : -threshold
  }
  return value
}

function formatCurrencyValue(
  value: number,
  options: ResolvedCurrencyFormatOptions,
  meta: DisplayMeta
): string {
  if (meta.kind === 'tokens') {
    if (options.compact) {
      return new Intl.NumberFormat(options.locale, {
        notation: 'compact',
        maximumFractionDigits: 1,
      }).format(value)
    }
    return formatNumberWithSuffix(
      value,
      options.digitsLarge,
      options.digitsSmall,
      options.abbreviate
    )
  }

  const digits = getFractionDigits(
    value,
    options.digitsLarge,
    options.digitsSmall
  )
  const adjustedValue = adjustForMinimum(value, digits, options.minimumNonZero)

  if (meta.kind === 'currency') {
    if (!options.showSymbol) {
      return new Intl.NumberFormat(options.locale, {
        notation: options.compact ? 'compact' : 'standard',
        minimumFractionDigits: 0,
        maximumFractionDigits: options.compact ? 1 : digits,
      }).format(adjustedValue)
    }

    const formatted = new Intl.NumberFormat(options.locale, {
      style: 'currency',
      currency: meta.currencyCode,
      currencyDisplay: 'narrowSymbol',
      notation: options.compact ? 'compact' : 'standard',
      minimumFractionDigits: 0,
      maximumFractionDigits: options.compact ? 1 : digits,
    }).format(adjustedValue)
    return formatted
  }

  const decimal = new Intl.NumberFormat(options.locale, {
    notation: options.compact ? 'compact' : 'standard',
    minimumFractionDigits: 0,
    maximumFractionDigits: options.compact ? 1 : digits,
  }).format(adjustedValue)

  return options.showSymbol ? `${meta.symbol} ${decimal}` : decimal
}

/**
 * Get the current currency configuration and display metadata.
 *
 * @returns Object containing config and display metadata
 *
 * @internal
 * This is primarily for internal use. Most consumers should use the
 * higher-level formatting functions instead.
 */
export function getCurrencyDisplay() {
  const config = getConfig()
  const meta = getDisplayMeta(config)
  return { config, meta }
}

/**
 * Format a USD amount according to the admin-configured display settings.
 *
 * This is the PRIMARY function for displaying quota/balance/credit amounts
 * that are stored in the system as USD values.
 *
 * @param amountUSD - Amount in system USD units (e.g., user balance, quota)
 * @param options - Optional formatting configuration
 * @returns Formatted string with currency symbol or token count
 *
 * @example
 * // With quotaDisplayType: 'USD'
 * formatCurrencyFromUSD(10) → "$10"
 *
 * @example
 * // With quotaDisplayType: 'CNY', usdExchangeRate: 7
 * formatCurrencyFromUSD(10) → "¥70"
 *
 * @example
 * // With quotaDisplayType: 'TOKENS', quotaPerUnit: 500000
 * formatCurrencyFromUSD(10) → "5,000,000"
 *
 * @example
 * // With quotaDisplayType: 'CUSTOM', customCurrencySymbol: '€', customCurrencyExchangeRate: 0.9
 * formatCurrencyFromUSD(10) → "€9"
 *
 * @remarks
 * Use this function for:
 * - User balance/quota display
 * - Recharge option amounts (before exchange rate applied)
 * - Transaction amounts in billing history
 * - Any value stored in database as USD
 *
 * DO NOT use for:
 * - Payment amounts already converted via priceRatio → use formatLocalCurrencyAmount()
 * - Raw token values → use formatQuotaWithCurrency()
 */
export function formatCurrencyFromUSD(
  amountUSD: number | null | undefined,
  options?: CurrencyFormatOptions
): string {
  if (amountUSD == null || Number.isNaN(amountUSD)) return '-'

  const { config, meta } = getCurrencyDisplay()
  const merged = mergeOptions(options)

  if (meta.kind === 'tokens') {
    const tokens = amountUSD * config.quotaPerUnit
    if (merged.compact) {
      return new Intl.NumberFormat(merged.locale, {
        notation: 'compact',
        maximumFractionDigits: 1,
      }).format(tokens)
    }
    return formatNumberWithSuffix(
      tokens,
      0,
      merged.digitsSmall,
      merged.abbreviate
    )
  }

  const value =
    meta.kind === 'currency'
      ? amountUSD * meta.exchangeRate
      : amountUSD * meta.exchangeRate

  return formatCurrencyValue(value, merged, meta)
}

/**
 * Format USD amounts for billing/payment contexts (never shows tokens).
 *
 * Similar to formatCurrencyFromUSD, but NEVER displays in token units.
 * Always shows real currency values (USD, CNY, etc.) even when the system
 * is configured to display quotas as tokens elsewhere.
 *
 * @param amountUSD - Amount in system USD units
 * @param options - Optional formatting configuration
 * @returns Formatted string with currency symbol (never tokens)
 *
 * @example
 * // With quotaDisplayType: 'TOKENS' - still shows currency
 * formatBillingCurrencyFromUSD(10) → "$10"  (not "5,000,000 tokens")
 *
 * @example
 * // With quotaDisplayType: 'CNY', usdExchangeRate: 7
 * formatBillingCurrencyFromUSD(10) → "¥70"
 *
 * @remarks
 * Use this function for:
 * - Model pricing displays
 * - API usage costs
 * - Billing/invoice amounts
 * - Any monetary value where tokens don't make sense
 *
 * DO NOT use for:
 * - User balance/quota → use formatCurrencyFromUSD()
 * - Payment amounts already in local currency → use formatLocalCurrencyAmount()
 */
export function formatBillingCurrencyFromUSD(
  amountUSD: number | null | undefined,
  options?: CurrencyFormatOptions
): string {
  if (amountUSD == null || Number.isNaN(amountUSD)) return '-'

  const { config } = getCurrencyDisplay()
  const meta = getBillingDisplayMeta(config)
  const merged = mergeOptions(options)
  const value =
    meta.kind === 'currency' || meta.kind === 'custom'
      ? amountUSD * meta.exchangeRate
      : amountUSD

  return formatCurrencyValue(value, merged, meta)
}

/**
 * Format raw quota values (token units) to display currency.
 *
 * Converts raw quota/token amounts to USD first, then formats according
 * to display settings. Use when you have quota in token units (e.g., 5000000)
 * and need to display it as currency (e.g., "$10").
 *
 * @param quota - Raw quota amount in token units (e.g., 5000000)
 * @param options - Optional formatting configuration
 * @returns Formatted string with currency symbol or token count
 *
 * @example
 * // With quotaPerUnit: 500000, quotaDisplayType: 'USD'
 * formatQuotaWithCurrency(5000000) → "$10"
 *
 * @example
 * // With quotaPerUnit: 500000, quotaDisplayType: 'CNY', usdExchangeRate: 7
 * formatQuotaWithCurrency(5000000) → "¥70"
 *
 * @remarks
 * Use this function for:
 * - Raw quota values from database (stored as tokens)
 * - When you need to convert tokens → USD → display currency
 *
 * DO NOT use for:
 * - Values already in USD → use formatCurrencyFromUSD()
 * - Payment amounts → use formatLocalCurrencyAmount()
 */
export function formatQuotaWithCurrency(
  quota: number | null | undefined,
  options?: CurrencyFormatOptions
): string {
  if (quota == null || Number.isNaN(quota)) return '-'

  const { config } = getCurrencyDisplay()
  const amountUSD = quota / config.quotaPerUnit
  return formatCurrencyFromUSD(amountUSD, options)
}

/**
 * Get the current currency label for UI display.
 *
 * Returns a simple string label representing the current display currency.
 * Useful for labels, tooltips, and UI text.
 *
 * @returns Currency label string (e.g., "USD", "CNY", "Tokens")
 *
 * @example
 * getCurrencyLabel() → "USD"
 * getCurrencyLabel() → "CNY"
 * getCurrencyLabel() → "Tokens"
 *
 * @remarks
 * Use this for:
 * - Currency selector labels
 * - Table column headers
 * - Form field labels
 */
export function getCurrencyLabel(): string {
  const { config, meta } = getCurrencyDisplay()

  if (meta.kind === 'tokens') {
    return 'Tokens'
  }

  switch (config.quotaDisplayType) {
    case 'CNY':
      return 'CNY'
    case 'CUSTOM':
      return meta.kind === 'custom' ? meta.symbol : 'Custom'
    case 'USD':
    default:
      return 'USD'
  }
}

/**
 * Check if currency display is enabled (not in token-only mode).
 *
 * @returns True if displaying in actual currency (USD/CNY/etc), false if tokens only
 *
 * @example
 * // With quotaDisplayType: 'USD' or 'CNY'
 * isCurrencyDisplayEnabled() → true
 *
 * // With quotaDisplayType: 'TOKENS'
 * isCurrencyDisplayEnabled() → false
 *
 * @remarks
 * Use this to conditionally show currency-specific UI elements
 */
export function isCurrencyDisplayEnabled(): boolean {
  const { meta } = getCurrencyDisplay()
  return meta.kind !== 'tokens'
}

/**
 * Format an amount that is ALREADY in local currency.
 *
 * ⚠️ CRITICAL: This function does NOT apply exchange rate conversion.
 * Only use this for values that have already been converted to local currency
 * via priceRatio or other means.
 *
 * @param amount - Amount already in local currency units
 * @param options - Optional formatting configuration
 * @returns Formatted string with appropriate currency symbol
 *
 * @example
 * // Payment amount already calculated: 10 USD × priceRatio(5) = 50 CNY
 * // With quotaDisplayType: 'CNY'
 * formatLocalCurrencyAmount(50) → "¥50"
 * // NOT "¥350" (which would be 50 × 7 exchangeRate)
 *
 * @example
 * // With quotaDisplayType: 'USD'
 * formatLocalCurrencyAmount(10) → "$10"
 *
 * @remarks
 * Use this function for:
 * - Payment amounts calculated via priceRatio (amount × price)
 * - Actual money charged to user's payment method
 * - Values that are already in the target currency
 *
 * DO NOT use for:
 * - USD values that need conversion → use formatCurrencyFromUSD()
 * - Raw quota values → use formatQuotaWithCurrency()
 *
 * Common mistake:
 * ```ts
 * // ❌ WRONG - Double conversion
 * const payment = usdAmount * exchangeRate
 * formatLocalCurrencyAmount(payment) // Will apply exchange rate again!
 *
 * // ✅ CORRECT - Already in local currency
 * const payment = usdAmount * priceRatio
 * formatLocalCurrencyAmount(payment) // Just formats with symbol
 * ```
 */
export function formatLocalCurrencyAmount(
  amount: number | null | undefined,
  options?: CurrencyFormatOptions
): string {
  if (amount == null || Number.isNaN(amount)) return '-'

  const { config } = getCurrencyDisplay()
  const meta = getBillingDisplayMeta(config)
  const merged = mergeOptions(options)

  return formatCurrencyValue(amount, merged, meta)
}
