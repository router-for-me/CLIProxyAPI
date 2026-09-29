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

import { cn } from '@/lib/utils'

export function IconThemeSystem({
  className,
  ...props
}: SVGProps<SVGSVGElement>) {
  return (
    <svg
      data-name='icon-theme-system'
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 79.86 51.14'
      className={cn(
        'overflow-hidden rounded-md',
        'stroke-primary fill-primary group-data-unchecked:stroke-muted-foreground group-data-unchecked:fill-muted-foreground',
        className
      )}
      {...props}
    >
      <path opacity={0.2} d='M0 0.03H22.88V51.17H0z' />
      <circle
        cx={6.7}
        cy={7.04}
        r={3.54}
        fill='#fff'
        opacity={0.8}
        stroke='#fff'
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <path
        d='M18.12 6.39h-5.87c-.6 0-1.09-.45-1.09-1s.49-1 1.09-1h5.87c.6 0 1.09.45 1.09 1s-.49 1-1.09 1zM16.55 9.77h-4.24c-.55 0-1-.45-1-1s.45-1 1-1h4.24c.55 0 1 .45 1 1s-.45 1-1 1z'
        fill='#fff'
        stroke='none'
        opacity={0.75}
      />
      <path
        d='M18.32 17.37H4.59c-.69 0-1.25-.47-1.25-1.05s.56-1.05 1.25-1.05h13.73c.69 0 1.25.47 1.25 1.05s-.56 1.05-1.25 1.05z'
        fill='#fff'
        stroke='none'
        opacity={0.72}
      />
      <path
        d='M15.34 21.26h-11c-.55 0-1-.41-1-.91s.45-.91 1-.91h11c.55 0 1 .41 1 .91s-.45.91-1 .91z'
        fill='#fff'
        stroke='none'
        opacity={0.55}
      />
      <path
        d='M16.46 25.57H4.43c-.6 0-1.09-.44-1.09-.98s.49-.98 1.09-.98h12.03c.6 0 1.09.44 1.09.98s-.49.98-1.09.98z'
        fill='#fff'
        stroke='none'
        opacity={0.67}
      />
      <rect
        x={33.36}
        y={19.73}
        width={2.75}
        height={3.42}
        rx={0.33}
        ry={0.33}
        opacity={0.31}
        stroke='none'
      />
      <rect
        x={29.64}
        y={16.57}
        width={2.75}
        height={6.58}
        rx={0.33}
        ry={0.33}
        opacity={0.4}
        stroke='none'
      />
      <rect
        x={37.16}
        y={14.44}
        width={2.75}
        height={8.7}
        rx={0.33}
        ry={0.33}
        opacity={0.26}
        stroke='none'
      />
      <rect
        x={41.19}
        y={10.75}
        width={2.75}
        height={12.4}
        rx={0.33}
        ry={0.33}
        opacity={0.37}
        stroke='none'
      />
      <g>
        <circle cx={62.74} cy={16.32} r={8} opacity={0.25} />
        <path
          d='M62.74 16.32l4.1-6.87c1.19.71 2.18 1.72 2.86 2.92s1.04 2.57 1.04 3.95h-8z'
          opacity={0.45}
        />
      </g>
      <rect
        x={29.64}
        y={27.75}
        width={41.62}
        height={18.62}
        rx={1.69}
        ry={1.69}
        opacity={0.3}
        stroke='none'
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
    </svg>
  )
}
