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
import { createElement, type ReactNode } from 'react'

import { IconBadge, type IconBadgeTone } from '@/components/ui/icon-badge'
import { cn } from '@/lib/utils'

export const sideDrawerContentClassName = (className?: string) =>
  cn(
    'bg-background text-foreground flex h-dvh w-full flex-col gap-0 overflow-hidden p-0 shadow-none',
    className
  )

export const sideDrawerHeaderClassName = (className?: string) =>
  cn(
    'border-border/70 bg-background/95 border-b px-4 py-3 text-start backdrop-blur supports-[backdrop-filter]:bg-background/80 sm:px-6 sm:py-4',
    className
  )

export const sideDrawerFormClassName = (className?: string) =>
  cn(
    'flex min-h-0 flex-1 flex-col gap-6 overflow-y-auto overscroll-contain px-4 py-4 sm:px-6 sm:py-5',
    className
  )

export const sideDrawerFooterClassName = (className?: string) =>
  cn(
    'border-border/70 bg-background/95 grid grid-cols-2 gap-2 border-t px-4 py-3 backdrop-blur supports-[backdrop-filter]:bg-background/80 sm:flex sm:flex-row sm:justify-end sm:px-6 sm:py-4',
    className
  )

export const sideDrawerSectionClassName = (className?: string) =>
  cn(
    'border-border/60 flex flex-col gap-4 border-b pb-6 last:border-b-0 last:pb-0',
    className
  )

export const sideDrawerSwitchItemClassName = (className?: string) =>
  cn(
    'border-border/60 flex min-h-16 flex-row items-center justify-between gap-3 border-y py-3',
    className
  )

export function SideDrawerSection(props: {
  children: ReactNode
  className?: string
}) {
  return createElement(
    'section',
    { className: sideDrawerSectionClassName(props.className) },
    props.children
  )
}

export function SideDrawerSectionHeader(props: {
  title: ReactNode
  description?: ReactNode
  icon?: ReactNode
  iconTone?: IconBadgeTone
  className?: string
}) {
  return createElement(
    'div',
    { className: cn('flex items-start gap-3', props.className) },
    props.icon
      ? createElement(
          IconBadge,
          { tone: props.iconTone, size: 'md' },
          props.icon
        )
      : null,
    createElement(
      'div',
      { className: 'min-w-0 flex-1' },
      createElement(
        'h3',
        { className: 'text-sm leading-none font-semibold tracking-tight' },
        props.title
      ),
      props.description
        ? createElement(
            'p',
            { className: 'text-muted-foreground mt-1 text-xs leading-5' },
            props.description
          )
        : null
    )
  )
}
