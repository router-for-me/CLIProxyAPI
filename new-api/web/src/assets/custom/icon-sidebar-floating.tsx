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

export function IconSidebarFloating(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      data-name='icon-sidebar-floating'
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 79.86 51.14'
      {...props}
    >
      <rect
        x={5.89}
        y={5.15}
        width={19.74}
        height={40}
        rx={2}
        ry={2}
        opacity={0.8}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <g stroke='#fff' strokeLinecap='round' strokeMiterlimit={10}>
        <path
          fill='none'
          opacity={0.72}
          strokeWidth='2px'
          d='M9.81 18.36L22.04 18.36'
        />
        <path
          fill='none'
          opacity={0.48}
          strokeWidth='2px'
          d='M9.81 25.57L20.33 25.57'
        />
        <path
          fill='none'
          opacity={0.55}
          strokeWidth='2px'
          d='M9.81 21.85L19.18 21.85'
        />
        <circle cx={11.76} cy={10.88} r={2.54} fill='#fff' opacity={0.8} />
        <path
          fill='none'
          opacity={0.8}
          strokeWidth='2px'
          d='M16.31 9.62L22.04 9.62'
        />
        <path fill='none' opacity={0.6} d='M16.1 12.27L21.16 12.27' />
      </g>
      <path
        fill='none'
        opacity={0.62}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='3px'
        d='M30.59 9.62L35.85 9.62'
      />
      <rect
        x={29.94}
        y={13.42}
        width={26.03}
        height={2.73}
        rx={0.64}
        ry={0.64}
        opacity={0.44}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <rect
        x={29.94}
        y={19.28}
        width={43.11}
        height={25.87}
        rx={2}
        ry={2}
        opacity={0.3}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
    </svg>
  )
}
