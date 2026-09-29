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

export interface UseCountdownOptions {
  initialSeconds?: number
  autoStart?: boolean
}

export function useCountdown(options: UseCountdownOptions = {}) {
  const { initialSeconds = 30, autoStart = false } = options
  const [secondsLeft, setSecondsLeft] = useState<number>(initialSeconds)
  const [isActive, setIsActive] = useState<boolean>(autoStart)
  const timerRef = useRef<number | null>(null)

  const clearTimer = useCallback(() => {
    if (timerRef.current) {
      window.clearInterval(timerRef.current)
      timerRef.current = null
    }
  }, [])

  const stop = useCallback(() => {
    clearTimer()
    setIsActive(false)
  }, [clearTimer])

  const start = useCallback(
    (seconds?: number) => {
      const total = seconds ?? initialSeconds
      setSecondsLeft(total)
      setIsActive(true)
      clearTimer()
      timerRef.current = window.setInterval(() => {
        setSecondsLeft((s) => {
          if (s <= 1) {
            clearTimer()
            setIsActive(false)
            return initialSeconds
          }
          return s - 1
        })
      }, 1000)
    },
    [clearTimer, initialSeconds]
  )

  const reset = useCallback(() => {
    stop()
    setSecondsLeft(initialSeconds)
  }, [initialSeconds, stop])

  useEffect(() => {
    return () => clearTimer()
  }, [clearTimer])

  return { secondsLeft, isActive, start, stop, reset }
}
