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
import { cva, type VariantProps } from 'class-variance-authority'
import React from 'react'

import { cn } from '@/lib/utils'

const mockupVariants = cva(
  'flex relative z-10 overflow-hidden shadow-2xl border border-border/70 dark:border-border/5 dark:border-t-border/15',
  {
    variants: {
      type: {
        mobile: 'rounded-4xl max-w-[350px]',
        responsive: 'rounded-md',
      },
    },
    defaultVariants: {
      type: 'responsive',
    },
  }
)

export interface MockupProps
  extends
    React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof mockupVariants> {}

export function Mockup({ className, type, ...props }: MockupProps) {
  return (
    <div
      data-slot='mockup'
      className={cn(mockupVariants({ type, className }))}
      {...props}
    />
  )
}

const frameVariants = cva(
  'bg-border/50 flex relative z-10 overflow-hidden rounded-2xl dark:bg-border/10',
  {
    variants: {
      size: {
        small: 'p-2',
        large: 'p-4',
      },
    },
    defaultVariants: {
      size: 'small',
    },
  }
)

export interface MockupFrameProps
  extends
    React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof frameVariants> {}

export function MockupFrame({ className, size, ...props }: MockupFrameProps) {
  return (
    <div
      data-slot='mockup-frame'
      className={cn(frameVariants({ size, className }))}
      {...props}
    />
  )
}
