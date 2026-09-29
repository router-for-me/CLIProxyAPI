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

export function IconSidebarInset(props: SVGProps<SVGSVGElement>) {
  return (
    <svg
      data-name='icon-sidebar-inset'
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 79.86 51.14'
      {...props}
    >
      <rect
        x={23.39}
        y={5.57}
        width={50.22}
        height={40}
        rx={2}
        ry={2}
        opacity={0.2}
        strokeLinecap='round'
        strokeMiterlimit={10}
      />
      <path
        fill='none'
        opacity={0.72}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.08 17.05L17.31 17.05'
      />
      <path
        fill='none'
        opacity={0.48}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.08 24.25L15.6 24.25'
      />
      <path
        fill='none'
        opacity={0.55}
        strokeLinecap='round'
        strokeMiterlimit={10}
        strokeWidth='2px'
        d='M5.08 20.54L14.46 20.54'
      />
      <g strokeLinecap='round' strokeMiterlimit={10}>
        <circle cx={7.04} cy={9.57} r={2.54} opacity={0.8} />
        <path
          fill='none'
          opacity={0.8}
          strokeWidth='2px'
          d='M11.59 8.3L17.31 8.3'
        />
        <path fill='none' opacity={0.6} d='M11.38 10.95L16.44 10.95' />
      </g>
    </svg>
  )
}
