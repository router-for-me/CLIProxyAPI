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
import type { ReactNode } from 'react'

import { getLobeIcon } from '@/lib/lobe-icon'
import { cn } from '@/lib/utils'

import { StatusBadge, type StatusBadgeProps } from './status-badge'

type ProviderBadgeProps = Omit<StatusBadgeProps, 'children' | 'label'> & {
  iconNode?: ReactNode
  iconKey?: string | null
  iconSize?: number
  label: string
  /** Color the label text by provider name. Set false for a neutral label. */
  colorText?: boolean
}

export function ProviderBadge({
  className,
  iconKey,
  iconNode,
  iconSize = 14,
  label,
  colorText = true,
  ...badgeProps
}: ProviderBadgeProps) {
  const icon = iconNode ?? (iconKey ? getLobeIcon(iconKey, iconSize) : null)

  return (
    <div
      data-slot='provider-badge'
      className={cn('flex max-w-full min-w-0 items-center gap-1.5', className)}
    >
      {icon && <span className='flex shrink-0 items-center'>{icon}</span>}
      <StatusBadge
        label={label}
        autoColor={colorText ? label : undefined}
        variant={colorText ? undefined : 'neutral'}
        size='sm'
        className={cn('min-w-0 shrink overflow-hidden', !icon && 'pl-0')}
        {...badgeProps}
      />
    </div>
  )
}
