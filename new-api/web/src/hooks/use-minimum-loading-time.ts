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
import { useState, useEffect, useRef } from 'react'

/**
 * Ensures a loading skeleton is shown for at least `minimumTime` ms
 * to prevent flickering when data loads too quickly.
 */
export function useMinimumLoadingTime(
  loading: boolean,
  minimumTime = 1000
): boolean {
  const [showSkeleton, setShowSkeleton] = useState(loading)
  // eslint-disable-next-line react-hooks/purity
  const loadingStartRef = useRef(Date.now())

  useEffect(() => {
    if (loading) {
      loadingStartRef.current = Date.now()
      setShowSkeleton(true)
    } else {
      const elapsed = Date.now() - loadingStartRef.current
      const remaining = Math.max(0, minimumTime - elapsed)

      if (remaining === 0) {
        setShowSkeleton(false)
      } else {
        const timer = setTimeout(() => setShowSkeleton(false), remaining)
        return () => clearTimeout(timer)
      }
    }
  }, [loading, minimumTime])

  return showSkeleton
}
