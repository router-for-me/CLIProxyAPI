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

export function IconLinuxDo({ className, ...props }: SVGProps<SVGSVGElement>) {
  return (
    <svg
      role='img'
      viewBox='0 0 16 16'
      xmlns='http://www.w3.org/2000/svg'
      width='16'
      height='16'
      className={cn(className)}
      {...props}
    >
      <title>LinuxDO</title>
      <g>
        <path
          d='M7.44 0h.13c.09 0 .19 0 .28 0h.43c.09 0 .18 0 .27 0h.25c.09 0 .17.03.26.08.15.03.29.06.44.08 1.97.38 3.78 1.47 4.95 3.11.04.06.09.12.13.18.67.96 1.15 2.11 1.3 3.28v.26c0 .15 0 .29 0 .44v.13c0 .09 0 .19 0 .28v.43c0 .09 0 .18 0 .27v.25c0 .09-.03.17-.08.26-.03.15-.06.29-.08.44-.38 1.97-1.47 3.78-3.11 4.95-.06.04-.12.09-.18.13-.96.67-2.11 1.15-3.28 1.3h-.26c-.15 0-.29 0-.44 0h-.13c-.09 0-.19 0-.28 0h-.43c-.09 0-.18 0-.27 0h-.25c-.09 0-.17-.03-.26-.08-.15-.03-.29-.06-.44-.08-1.97-.38-3.78-1.47-4.95-3.11L.59 12.6c-.67-.96-1.15-2.11-1.3-3.28v-.26c0-.15 0-.29 0-.44v-.13c0-.09 0-.19 0-.28v-.43c0-.09 0-.18 0-.27v-.25c0-.09.03-.17.08-.26.03-.15.06-.29.08-.44.38-1.97 1.47-3.78 3.11-4.95.06-.04.12-.09.18-.13C4.42.73 5.57.26 6.74.1c.26-.03.41-.1.7-.1Z'
          fill='#efefef'
        />
        <path
          d='M1.27 11.33h13.45c-.94 1.89-2.51 3.21-4.51 3.88-1.99.59-3.96.37-5.8-.57-1.25-.7-2.67-1.9-3.14-3.3Z'
          fill='#feb005'
        />
        <path
          d='M12.54 1.99c.87.7 1.82 1.59 2.18 2.68H1.27c.87-1.74 2.33-3.13 4.2-3.78 2.44-.79 5-.47 7.07 1.1Z'
          fill='#1d1d1f'
        />
      </g>
    </svg>
  )
}
