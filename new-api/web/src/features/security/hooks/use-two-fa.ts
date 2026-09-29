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
import { useState, useEffect, useCallback } from 'react'

import type { TwoFAStatus } from '@/features/profile/types'
import { AuthOperationError } from '@/lib/secure-verification'

import { get2FAStatus } from '../api'

// ============================================================================
// Two-FA Hook
// ============================================================================

const DEFAULT_STATUS: TwoFAStatus = {
  enabled: false,
  locked: false,
  backup_codes_remaining: 0,
}

export function useTwoFA(enabled = true) {
  const [loading, setLoading] = useState(true)
  const [status, setStatus] = useState<TwoFAStatus>(DEFAULT_STATUS)
  const [error, setError] = useState<string>()

  const fetchStatus = useCallback(async () => {
    if (!enabled) return

    try {
      setLoading(true)
      setError(undefined)
      setStatus(await get2FAStatus())
    } catch (error) {
      setError(AuthOperationError.from(error).message)
    } finally {
      setLoading(false)
    }
  }, [enabled])

  useEffect(() => {
    fetchStatus()
  }, [fetchStatus])

  return {
    status,
    loading,
    error,
    refetch: fetchStatus,
  }
}
