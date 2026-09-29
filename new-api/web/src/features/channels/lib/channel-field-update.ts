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

export const CHANNEL_FIELD_UPDATE_DELAY_MS = 800

interface ChannelFieldUpdateTimers {
  setTimeout: (callback: () => void, delay: number) => number
  clearTimeout: (id: number) => void
}

const browserTimers: ChannelFieldUpdateTimers = {
  setTimeout: (callback, delay) => window.setTimeout(callback, delay),
  clearTimeout: (id) => window.clearTimeout(id),
}

export function createChannelFieldUpdateScheduler(
  onUpdate: (value: number) => void,
  timers: ChannelFieldUpdateTimers = browserTimers
) {
  let timeoutId: number | undefined
  let pendingValue: number | undefined

  const clearPendingTimer = () => {
    if (timeoutId === undefined) return
    timers.clearTimeout(timeoutId)
    timeoutId = undefined
  }

  const commitPendingValue = () => {
    clearPendingTimer()
    if (pendingValue === undefined) return

    const value = pendingValue
    pendingValue = undefined
    onUpdate(value)
  }

  return {
    schedule(value: number) {
      clearPendingTimer()
      pendingValue = value
      timeoutId = timers.setTimeout(
        commitPendingValue,
        CHANNEL_FIELD_UPDATE_DELAY_MS
      )
    },
    flush: commitPendingValue,
  }
}
