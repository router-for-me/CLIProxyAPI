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
import * as React from 'react'

import { cn } from '@/lib/utils'

type BadgeCellProps = React.HTMLAttributes<HTMLDivElement>

export function BadgeCell({ className, ...props }: BadgeCellProps) {
  return (
    <div
      data-slot='badge-cell'
      className={cn(
        '-ml-1.5 flex max-w-full min-w-0 items-center gap-1 overflow-hidden [&_[data-slot=status-badge]]:max-w-full [&_[data-slot=status-badge]]:min-w-0',
        className
      )}
      {...props}
    />
  )
}
