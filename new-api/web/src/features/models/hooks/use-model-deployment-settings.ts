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
import { useCallback, useEffect, useRef, useState } from 'react'

import { getDeploymentSettings, testDeploymentConnection } from '../api'

interface ConnectionState {
  loading: boolean
  ok: boolean | null
  error: string | null
}

// Connection cache (5 minutes TTL)
const CONNECTION_CACHE_TTL = 5 * 60 * 1000
let connectionCache: {
  ok: boolean
  timestamp: number
} | null = null

function getCachedConnection(): boolean | null {
  if (!connectionCache) return null
  if (Date.now() - connectionCache.timestamp > CONNECTION_CACHE_TTL) {
    connectionCache = null
    return null
  }
  return connectionCache.ok
}

function setCachedConnection(ok: boolean) {
  connectionCache = { ok, timestamp: Date.now() }
}

export function clearConnectionCache() {
  connectionCache = null
}

type LoadingPhase = 'idle' | 'settings' | 'connection' | 'done'

export function useModelDeploymentSettings() {
  const [loading, setLoading] = useState(true)
  const [loadingPhase, setLoadingPhase] = useState<LoadingPhase>('settings')
  const [settings, setSettings] = useState<Record<string, unknown>>({
    'model_deployment.ionet.enabled': false,
  })
  const [connectionState, setConnectionState] = useState<ConnectionState>({
    loading: false,
    ok: null,
    error: null,
  })
  const initialLoadRef = useRef(true)

  // Parallel fetch: settings + connection test (when enabled)
  const fetchAll = useCallback(async (useCache = true) => {
    setLoading(true)
    setLoadingPhase('settings')

    try {
      // Step 1: Fetch settings first (usually fast)
      const response = await getDeploymentSettings()
      const isEnabled = response?.success && response?.data?.enabled === true

      setSettings({
        'model_deployment.ionet.enabled': isEnabled,
      })

      if (!isEnabled) {
        // Not enabled, done
        setConnectionState({ loading: false, ok: null, error: null })
        setLoadingPhase('done')
        setLoading(false)
        return
      }

      // Step 2: Check connection (check cache first)
      if (useCache) {
        const cached = getCachedConnection()
        if (cached !== null) {
          setConnectionState({ loading: false, ok: cached, error: null })
          setLoadingPhase('done')
          setLoading(false)
          return
        }
      }

      // Test connection
      setLoadingPhase('connection')
      setConnectionState({ loading: true, ok: null, error: null })

      try {
        const connResponse = await testDeploymentConnection()
        if (connResponse?.success) {
          setCachedConnection(true)
          setConnectionState({ loading: false, ok: true, error: null })
        } else {
          const message = connResponse?.message || 'Connection failed'
          setCachedConnection(false)
          setConnectionState({ loading: false, ok: false, error: message })
        }
      } catch (error: unknown) {
        const errMsg =
          error instanceof Error ? error.message : 'Connection failed'
        setCachedConnection(false)
        setConnectionState({ loading: false, ok: false, error: errMsg })
      }
    } catch {
      // Settings fetch failed, use defaults
      setConnectionState({ loading: false, ok: null, error: null })
    } finally {
      setLoadingPhase('done')
      setLoading(false)
    }
  }, [])

  // Initial load
  useEffect(() => {
    if (initialLoadRef.current) {
      initialLoadRef.current = false
      fetchAll(true)
    }
  }, [fetchAll])

  const isIoNetEnabled = Boolean(settings['model_deployment.ionet.enabled'])

  // Manual retry (skip cache)
  const testConnection = useCallback(async () => {
    clearConnectionCache()
    setConnectionState({ loading: true, ok: null, error: null })
    setLoadingPhase('connection')

    try {
      const response = await testDeploymentConnection()
      if (response?.success) {
        setCachedConnection(true)
        setConnectionState({ loading: false, ok: true, error: null })
        return
      }
      const message = response?.message || 'Connection failed'
      setCachedConnection(false)
      setConnectionState({ loading: false, ok: false, error: message })
    } catch (error: unknown) {
      const errMsg =
        error instanceof Error ? error.message : 'Connection failed'
      setCachedConnection(false)
      setConnectionState({ loading: false, ok: false, error: errMsg })
    } finally {
      setLoadingPhase('done')
    }
  }, [])

  // Refresh all (skip cache)
  const refresh = useCallback(() => {
    clearConnectionCache()
    return fetchAll(false)
  }, [fetchAll])

  // Refresh on window focus (useful after saving settings in another page)
  useEffect(() => {
    const handler = () => {
      // Use cache on focus to avoid unnecessary requests
      fetchAll(true)
    }
    window.addEventListener('focus', handler)
    return () => window.removeEventListener('focus', handler)
  }, [fetchAll])

  return {
    loading,
    loadingPhase,
    settings,
    isIoNetEnabled,
    refresh,
    connectionLoading: connectionState.loading,
    connectionOk: connectionState.ok,
    connectionError: connectionState.error,
    testConnection,
  }
}
