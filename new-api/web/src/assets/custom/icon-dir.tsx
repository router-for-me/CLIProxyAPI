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
import { type SVGProps } from 'react'

import { type Direction } from '@/context/direction-provider'
import { cn } from '@/lib/utils'

type IconDirProps = SVGProps<SVGSVGElement> & {
  dir: Direction
}

export function IconDir({ dir, className, ...props }: IconDirProps) {
  return (
    <svg
      data-name={`icon-dir-${dir}`}
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 79.86 51.14'
      className={cn(dir === 'rtl' && 'rotate-y-180', className)}
      {...props}
    >
      <path
        d='M23.42.51h51.92c2.21 0 4 1.79 4 4v42.18c0 2.21-1.79 4-4 4H23.42s-.04-.02-.04-.04V.55s.02-.04.04-.04z'
        opacity={0.15}
      />
      <path
        fill='none'
        opacity={0.72}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.56 14.88L17.78 14.88'
      />
      <path
        fill='none'
        opacity={0.48}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.56 22.09L16.08 22.09'
      />
      <path
        fill='none'
        opacity={0.55}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.56 18.38L14.93 18.38'
      />
      <g strokeLinecap='round' strokeMiterlimit={10}>
        <circle cx={7.51} cy={7.4} r={2.54} opacity={0.8} />
        <path
          fill='none'
          opacity={0.8}
          strokeWidth='2px'
          d='M12.06 6.14L17.78 6.14'
        />
        <path fill='none' opacity={0.6} d='M11.85 8.79L16.91 8.79' />
      </g>
      <path
        fill='none'
        opacity={0.62}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='3px'
        d='M29.41 7.4L34.67 7.4'
      />
      <rect
        x={28.76}
        y={11.21}
        width={26.03}
        height={2.73}
        rx={0.64}
        ry={0.64}
        opacity={0.44}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <rect
        x={28.76}
        y={17.01}
        width={44.25}
        height={13.48}
        rx={0.64}
        ry={0.64}
        opacity={0.3}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <rect
        x={28.76}
        y={33.57}
        width={44.25}
        height={4.67}
        rx={0.64}
        ry={0.64}
        opacity={0.21}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <rect
        x={28.76}
        y={41.32}
        width={36.21}
        height={4.67}
        rx={0.64}
        ry={0.64}
        opacity={0.3}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
    </svg>
  )
}
