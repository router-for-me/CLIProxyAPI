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
import type { PingStatus } from '@/features/dashboard/types'

/**
 * Get color class for latency status
 */
export function getLatencyColorClass(latency: number): string {
  if (latency < 200) {
    return 'text-green-600 dark:text-green-400'
  }
  if (latency < 500) {
    return 'text-yellow-600 dark:text-yellow-400'
  }
  return 'text-red-600 dark:text-red-400'
}

/**
 * Test URL latency
 */
export async function testUrlLatency(url: string): Promise<PingStatus> {
  try {
    const startTime = performance.now()
    await fetch(url, {
      method: 'HEAD',
      mode: 'no-cors',
      cache: 'no-cache',
    })
    const endTime = performance.now()
    const latency = Math.round(endTime - startTime)

    return { latency, testing: false, error: false }
  } catch (_error) {
    return { latency: null, testing: false, error: true }
  }
}

/**
 * Open external speed test link
 */
export function openExternalSpeedTest(url: string): void {
  const encodedUrl = encodeURIComponent(url)
  const speedTestUrl = `https://www.tcptest.cn/http/${encodedUrl}`
  window.open(speedTestUrl, '_blank', 'noopener,noreferrer')
}

/**
 * Get default ping status
 */
export function getDefaultPingStatus(): PingStatus {
  return {
    latency: null,
    testing: false,
    error: false,
  }
}
