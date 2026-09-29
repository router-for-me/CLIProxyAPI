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
import React from 'react'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'

export interface LogoProps extends React.HTMLAttributes<HTMLDivElement> {
  image: React.ComponentType<React.SVGProps<SVGSVGElement>>
  name: string
  version?: string
  width?: number
  height?: number
  showName?: boolean
  badge?: string
}

export function Logo({
  className,
  image: SvgImage,
  name,
  version,
  width = 24,
  height = 24,
  showName = true,
  badge,
  ...props
}: LogoProps) {
  return (
    <div
      data-slot='logo'
      className={cn('flex items-center gap-2 text-sm font-medium', className)}
      {...props}
    >
      <SvgImage
        width={width}
        height={height}
        aria-hidden='true'
        className='max-h-full max-w-full opacity-70'
      />
      <span className={cn(!showName && 'sr-only')}>{name}</span>
      {version && <span className='text-muted-foreground'>{version}</span>}
      {badge && <Badge variant='secondary'>{badge}</Badge>}
    </div>
  )
}
