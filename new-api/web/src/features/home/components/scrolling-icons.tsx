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

import { IconCard } from './icon-card'

interface ScrollingIconsProps {
  icons: readonly string[]
  direction?: 'up' | 'down'
  className?: string
}

/**
 * Scrolling icon column with seamless loop animation
 */
export function ScrollingIcons({
  icons,
  direction = 'up',
  className,
}: ScrollingIconsProps) {
  const animationClass =
    direction === 'up' ? 'animate-scroll-up' : 'animate-scroll-down'

  return (
    <div
      className={cn(
        'scroll-container hidden h-[360px] overflow-hidden lg:block',
        className
      )}
    >
      <div className={cn('flex flex-col gap-5', animationClass)}>
        {/* First set */}
        {icons.map((iconName, i) => (
          <IconCard key={`${direction}-1-${i}`} iconName={iconName} />
        ))}
        {/* Duplicate set for seamless loop */}
        {icons.map((iconName, i) => (
          <IconCard
            key={`${direction}-2-${i}`}
            iconName={iconName}
            className='aria-hidden'
          />
        ))}
      </div>
    </div>
  )
}
