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
import type { QueryClient } from '@tanstack/react-query'
import i18next from 'i18next'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'

import { deleteVendor as deleteVendorAPI } from '../api'
import { vendorsQueryKeys, modelsQueryKeys } from './query-keys'

// ============================================================================
// Vendor Actions
// ============================================================================

/**
 * Delete a vendor
 */
export async function handleDeleteVendor(
  id: number,
  queryClient?: QueryClient,
  onSuccess?: () => void
): Promise<void> {
  try {
    const response = await deleteVendorAPI(id)
    if (response.success) {
      toast.success(i18next.t('Vendor deleted successfully'))
      queryClient?.invalidateQueries({ queryKey: vendorsQueryKeys.lists() })
      queryClient?.invalidateQueries({ queryKey: modelsQueryKeys.lists() })
      onSuccess?.()
    } else {
      handleServerError(response, i18next.t('Failed to delete vendor'))
    }
  } catch (error: unknown) {
    handleServerError(error, i18next.t('Failed to delete vendor'))
  }
}
