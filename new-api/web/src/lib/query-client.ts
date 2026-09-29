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
import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query'

import { handleServerError } from './handle-server-error'
import { getServerErrorStatus } from './server-error-message'

interface ErrorNotificationMeta extends Record<string, unknown> {
  /** Disable automatic notifications when a caller presents the failure itself. */
  errorToast?: boolean
}

declare module '@tanstack/react-query' {
  interface Register {
    queryMeta: ErrorNotificationMeta
    mutationMeta: ErrorNotificationMeta
  }
}

export function createAppQueryClient(
  onInternalServerError?: () => void
): QueryClient {
  return new QueryClient({
    defaultOptions: {
      queries: {
        retry: (failureCount, error) => {
          if (import.meta.env.DEV || failureCount > 3) return false
          return ![401, 403].includes(getServerErrorStatus(error) ?? 0)
        },
        refetchOnWindowFocus: false,
        staleTime: 10 * 1000,
      },
    },
    mutationCache: new MutationCache({
      onError: (error, _variables, _context, mutation) => {
        // A mutation-specific callback owns its fallback or inline error state.
        if (mutation.meta?.errorToast !== false && !mutation.options.onError) {
          handleServerError(error)
        }
      },
    }),
    queryCache: new QueryCache({
      onError: (error, query) => {
        if (query.meta?.errorToast !== false) handleServerError(error)
        if (getServerErrorStatus(error) === 500) onInternalServerError?.()
      },
    }),
  })
}
