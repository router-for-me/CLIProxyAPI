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
import { queryOptions, type QueryClient } from '@tanstack/react-query'

import { getStatus } from '@/lib/api'
import { DEFAULT_SYSTEM_NAME, DEFAULT_LOGO } from '@/lib/constants'
import {
  useSystemConfigStore,
  type CurrencyConfig,
  type CurrencyDisplayType,
  type SystemConfig,
  DEFAULT_CURRENCY_CONFIG,
} from '@/stores/system-config-store'

/**
 * Single source of truth for `/api/status`.
 *
 * `/api/status` is entirely global on the backend: every field is read from
 * in-memory option maps under a read lock, with no user context and no auth
 * middleware on the route. That makes it safe to share one cache entry across
 * every consumer — branding, nav module gates, and the setup guard.
 *
 * Anything that needs status must go through `statusQueryOptions` so React
 * Query can dedupe. Calling `getStatus()` directly re-introduces the duplicate
 * requests this module exists to collapse.
 */
export const STATUS_QUERY_KEY = ['status'] as const

export const STATUS_STORAGE_KEY = 'status'

/** Status payload shape — loose on purpose; the backend map is open-ended. */
export type StatusData = Record<string, unknown>

/** Coerce a status field to a number, keeping `fallback` for unusable values. */
function toNumber(value: unknown, fallback: number): number {
  if (typeof value === 'number' && !Number.isNaN(value)) return value
  if (typeof value === 'string') {
    const parsed = Number(value)
    if (!Number.isNaN(parsed)) return parsed
  }
  return fallback
}

/**
 * Map `/api/status` response data to our persisted system config structure
 */
export function mapStatusDataToConfig(
  data: StatusData | undefined | null
): Partial<SystemConfig> {
  if (!data) return {}

  const quotaDisplayType =
    (data.quota_display_type as CurrencyDisplayType | undefined) ??
    DEFAULT_CURRENCY_CONFIG.quotaDisplayType

  const currency: CurrencyConfig = {
    displayInCurrency:
      (data.display_in_currency as boolean | undefined) ??
      DEFAULT_CURRENCY_CONFIG.displayInCurrency,
    quotaDisplayType,
    quotaPerUnit: toNumber(
      data.quota_per_unit,
      DEFAULT_CURRENCY_CONFIG.quotaPerUnit
    ),
    usdExchangeRate: toNumber(
      data.usd_exchange_rate,
      DEFAULT_CURRENCY_CONFIG.usdExchangeRate
    ),
    customCurrencySymbol:
      (data.custom_currency_symbol as string | undefined)?.trim() ||
      DEFAULT_CURRENCY_CONFIG.customCurrencySymbol,
    customCurrencyExchangeRate: toNumber(
      data.custom_currency_exchange_rate,
      DEFAULT_CURRENCY_CONFIG.customCurrencyExchangeRate
    ),
  }

  return {
    systemName: (data.system_name as string | undefined) || DEFAULT_SYSTEM_NAME,
    logo: (data.logo as string | undefined) || DEFAULT_LOGO,
    footerHtml: data.footer_html as string | undefined,
    demoSiteEnabled: data.demo_site_enabled as boolean | undefined,
    displayTokenStatEnabled: data.display_token_stat_enabled as
      | boolean
      | undefined,
    currency,
  }
}

/** Read the last known status from localStorage (survives reload, may be stale). */
export function readCachedStatus(): StatusData | null {
  try {
    if (typeof window === 'undefined') return null
    const raw = window.localStorage.getItem(STATUS_STORAGE_KEY)
    return raw ? (JSON.parse(raw) as StatusData) : null
  } catch {
    return null
  }
}

/** Persist the latest status so the next cold start can render before fetching. */
function writeCachedStatus(status: StatusData | null): void {
  try {
    if (typeof window !== 'undefined' && status) {
      window.localStorage.setItem(STATUS_STORAGE_KEY, JSON.stringify(status))
    }
  } catch {
    /* Storage can be unavailable in private mode. */
  }
}

/**
 * The single `/api/status` request.
 *
 * Owns both side effects of a successful read — syncing the system-config store
 * and writing the localStorage snapshot — so consumers never repeat either one.
 */
async function fetchStatus(): Promise<StatusData | null> {
  const status = (await getStatus()) as StatusData | null

  if (status) {
    try {
      useSystemConfigStore.getState().setConfig(mapStatusDataToConfig(status))
    } catch (err) {
      if (import.meta.env.DEV) {
        // eslint-disable-next-line no-console
        console.warn('[status] Failed to sync status to system config', err)
      }
    }
    writeCachedStatus(status)
  }

  return status
}

export const statusQueryOptions = queryOptions({
  queryKey: STATUS_QUERY_KEY,
  queryFn: fetchStatus,
  // Data becomes stale after 5 minutes
  staleTime: 5 * 60 * 1000,
  // Cache expires after 30 minutes
  gcTime: 30 * 60 * 1000,
})

/**
 * Await status from the shared cache.
 *
 * Use this when a cached snapshot can be shown during a background refresh,
 * such as system configuration loading. Concurrent callers share one request.
 * Navigation guards use `fetchQuery(statusQueryOptions)` instead, because
 * redirects must wait for stale or invalidated access flags to refresh.
 *
 * Resolution rules, which are `ensureQueryData`'s and not `staleTime`'s:
 * - No cached entry: fetches and awaits the response.
 * - Cached entry, fresh: resolves from cache, no network.
 * - Cached entry, stale: resolves from cache *immediately* and kicks off a
 *   background refresh (`revalidateIfStale`). Consumers must subscribe to the
 *   updated query or system-config store to observe the refreshed data.
 *
 * The React Query cache is memory-only (no persister is installed), so a hard
 * reload always starts from an empty cache and fetches.
 */
export async function ensureStatus(
  queryClient: QueryClient
): Promise<StatusData | null> {
  return queryClient.ensureQueryData({
    ...statusQueryOptions,
    revalidateIfStale: true,
  })
}
