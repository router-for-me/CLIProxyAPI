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
import { useQuery } from '@tanstack/react-query'

import type { SystemStatus } from '@/features/auth/types'
import { readCachedStatus, statusQueryOptions } from '@/lib/status-query'

/** Seed value from the persisted snapshot, so the first render is not empty. */
function getInitialStatus(): SystemStatus | undefined {
  return (readCachedStatus() as SystemStatus | null) ?? undefined
}

/**
 * Subscribe to the shared `/api/status` query.
 *
 * Every caller reads the same cache entry, so mounting this hook in several
 * components costs one request. See `statusQueryOptions` for cache lifetimes.
 */
export function useStatus() {
  const { data, isLoading, error } = useQuery({
    ...statusQueryOptions,
    // Use localStorage data as initial data
    placeholderData: getInitialStatus(),
  })

  return {
    status: (data as SystemStatus | null) ?? null,
    loading: isLoading,
    error,
  }
}
