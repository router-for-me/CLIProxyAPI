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
import { useId, type SVGProps } from 'react'

type IconSub2apiProps = SVGProps<SVGSVGElement> & {
  size?: number
}

export function IconSub2api({ size = 20, ...props }: IconSub2apiProps) {
  const gradientId = useId()

  return (
    <svg
      xmlns='http://www.w3.org/2000/svg'
      viewBox='0 0 24 24'
      width={size}
      height={size}
      {...props}
    >
      <defs>
        <linearGradient
          id={gradientId}
          x1='4'
          y1='4'
          x2='20'
          y2='20'
          gradientUnits='userSpaceOnUse'
        >
          <stop stopColor='#67EDB1' />
          <stop offset='.48' stopColor='#2FD3E1' />
          <stop offset='1' stopColor='#2E68EA' />
        </linearGradient>
      </defs>
      <g
        fill='none'
        stroke={`url(#${gradientId})`}
        strokeLinecap='round'
        strokeLinejoin='round'
        strokeWidth='2.7'
      >
        <path d='m19.25 7.65-2.55-3.2a1.33 1.33 0 0 0-1.03-.5H8.58c-.34 0-.67.13-.91.37L4.15 7.65c-.93.88-.6 1.52.65 2.3l9.55 5.97' />
        <path d='m4.75 16.35 2.55 3.2c.25.31.63.5 1.03.5h7.09c.34 0 .67-.13.91-.37l3.52-3.33c.93-.88.6-1.52-.65-2.3L9.65 8.08' />
      </g>
    </svg>
  )
}
