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

interface ConnectionLineProps {
  direction?: 'left' | 'right'
}

/**
 * Connection line between gateway and icon columns
 */
export function ConnectionLine({ direction = 'left' }: ConnectionLineProps) {
  const gradientClass =
    direction === 'left'
      ? 'from-amber-500/60 to-amber-500/20'
      : 'from-amber-500/20 to-amber-500/60'

  return (
    <div className='hidden lg:block'>
      <div className={cn('h-[2px] w-24 bg-gradient-to-r', gradientClass)} />
    </div>
  )
}
