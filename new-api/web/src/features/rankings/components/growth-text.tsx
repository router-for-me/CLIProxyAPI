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
import { cn } from '@/lib/utils'

type GrowthTextProps = {
  value: number
  className?: string
}

/**
 * Render a period-over-period growth percent as `↑303%`, `↓12.4%`, or
 * `0%` (when no change). The arrow is encoded in the text so the value
 * still aligns inside a tabular column.
 */
export function GrowthText(props: GrowthTextProps) {
  const v = props.value
  if (!Number.isFinite(v) || v === 0) {
    return (
      <span
        className={cn(
          'text-muted-foreground/80 font-mono tabular-nums',
          props.className
        )}
      >
        0%
      </span>
    )
  }
  const isUp = v > 0
  return (
    <span
      className={cn(
        'font-mono tabular-nums',
        isUp
          ? 'text-emerald-600 dark:text-emerald-400'
          : 'text-rose-600 dark:text-rose-400',
        props.className
      )}
    >
      {isUp ? '↑' : '↓'}
      {Math.abs(v).toFixed(Math.abs(v) >= 100 ? 0 : 1)}%
    </span>
  )
}
