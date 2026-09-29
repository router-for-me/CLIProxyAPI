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

interface StatItemProps {
  value: string | number
  suffix?: string
  description?: string
}

const GRADIENT_TEXT =
  'from-foreground to-foreground/70 bg-gradient-to-r bg-clip-text font-bold text-transparent'

/**
 * Individual stat item with value, suffix, and description
 */
export function StatItem({ value, suffix, description }: StatItemProps) {
  return (
    <div className='flex flex-col items-center gap-2 text-center'>
      <div className='flex items-baseline gap-1'>
        <div
          className={cn(
            GRADIENT_TEXT,
            'text-4xl drop-shadow-sm transition-all duration-300 sm:text-5xl md:text-6xl'
          )}
        >
          {value}
        </div>
        {suffix && (
          <div
            className={cn(GRADIENT_TEXT, 'text-3xl sm:text-4xl md:text-5xl')}
          >
            {suffix}
          </div>
        )}
      </div>
      {description && (
        <p className='text-muted-foreground text-sm font-medium'>
          {description}
        </p>
      )}
    </div>
  )
}
