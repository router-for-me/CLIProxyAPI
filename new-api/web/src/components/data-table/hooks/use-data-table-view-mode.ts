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
import * as React from 'react'

export const DATA_TABLE_VIEW_MODES = {
  TABLE: 'table',
  CARD: 'card',
} as const

export type DataTableViewMode =
  (typeof DATA_TABLE_VIEW_MODES)[keyof typeof DATA_TABLE_VIEW_MODES]

function isViewMode(value: unknown): value is DataTableViewMode {
  return (
    value === DATA_TABLE_VIEW_MODES.TABLE ||
    value === DATA_TABLE_VIEW_MODES.CARD
  )
}

function readViewMode(
  storageKey: string | undefined,
  fallback: DataTableViewMode
): DataTableViewMode {
  if (!storageKey || typeof window === 'undefined') {
    return fallback
  }

  try {
    const raw = window.localStorage.getItem(storageKey)
    return isViewMode(raw) ? raw : fallback
  } catch {
    return fallback
  }
}

type UseDataTableViewModeOptions = {
  /**
   * localStorage key for persisting the selected view mode. When omitted the
   * selection lives only in memory (resets on reload).
   */
  storageKey?: string
  /** Initial mode used when nothing is persisted. Defaults to `'table'`. */
  defaultMode?: DataTableViewMode
}

/**
 * View-mode (table vs. card) state with optional per-table localStorage
 * persistence. Mirrors the SSR/try-catch guarded approach used for column
 * visibility persistence in {@link useDataTable}.
 */
export function useDataTableViewMode(
  options: UseDataTableViewModeOptions = {}
): [DataTableViewMode, (mode: DataTableViewMode) => void] {
  const defaultMode = options.defaultMode ?? DATA_TABLE_VIEW_MODES.TABLE
  const storageKey = options.storageKey

  const [viewMode, setViewModeState] = React.useState<DataTableViewMode>(() =>
    readViewMode(storageKey, defaultMode)
  )

  // Re-hydrate when the storage key changes (e.g. switching tables).
  const hydratedStorageKeyRef = React.useRef(storageKey)
  React.useEffect(() => {
    if (storageKey === hydratedStorageKeyRef.current) {
      return
    }
    hydratedStorageKeyRef.current = storageKey
    setViewModeState(readViewMode(storageKey, defaultMode))
  }, [storageKey, defaultMode])

  const setViewMode = React.useCallback(
    (mode: DataTableViewMode) => {
      setViewModeState(mode)
      if (!storageKey || typeof window === 'undefined') {
        return
      }
      try {
        window.localStorage.setItem(storageKey, mode)
      } catch {
        // Storage can be unavailable in private mode; controls still work.
      }
    },
    [storageKey]
  )

  return [viewMode, setViewMode]
}
