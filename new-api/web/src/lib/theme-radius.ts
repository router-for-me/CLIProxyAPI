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
import { useEffect, useState } from 'react'

export function resolveThemeRadiusPx(
  cssVariable = '--radius-md'
): number | undefined {
  if (typeof document === 'undefined') return undefined

  const probe = document.createElement('div')
  probe.style.borderRadius = `var(${cssVariable})`
  probe.style.pointerEvents = 'none'
  probe.style.position = 'absolute'
  probe.style.visibility = 'hidden'

  document.documentElement.appendChild(probe)
  const resolvedRadius = getComputedStyle(probe).borderTopLeftRadius
  probe.remove()

  const parsedRadius = Number.parseFloat(resolvedRadius)
  return Number.isFinite(parsedRadius) ? parsedRadius : undefined
}

export function useThemeRadiusPx(
  cssVariable = '--radius-md',
  refreshKey?: string
): number | undefined {
  const [radius, setRadius] = useState<number | undefined>()

  useEffect(() => {
    setRadius(resolveThemeRadiusPx(cssVariable))
  }, [cssVariable, refreshKey])

  return radius
}
