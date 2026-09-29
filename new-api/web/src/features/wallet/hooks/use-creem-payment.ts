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
import i18next from 'i18next'
import { useState, useCallback } from 'react'
import { toast } from 'sonner'

import { handleServerError } from '@/lib/handle-server-error'

import { requestCreemPayment, isApiSuccess } from '../api'

/**
 * Hook for handling Creem payment processing
 */
export function useCreemPayment() {
  const [processing, setProcessing] = useState(false)

  const processCreemPayment = useCallback(async (productId: string) => {
    setProcessing(true)
    try {
      const response = await requestCreemPayment({
        product_id: productId,
        payment_method: 'creem',
      })

      if (isApiSuccess(response) && response.data?.checkout_url) {
        window.open(response.data.checkout_url, '_blank')
        toast.success(i18next.t('Redirecting to Creem checkout...'))
        return true
      }

      handleServerError(response, i18next.t('Payment request failed'))
      return false
    } catch (_error) {
      handleServerError(_error, i18next.t('Payment request failed'))
      return false
    } finally {
      setProcessing(false)
    }
  }, [])

  return { processing, processCreemPayment }
}
