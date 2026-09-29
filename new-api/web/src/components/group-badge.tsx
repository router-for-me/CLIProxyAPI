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
import { useTranslation } from 'react-i18next'

import { cn } from '@/lib/utils'

import { StatusBadge, type StatusBadgeProps } from './status-badge'
import { Badge } from './ui/badge'

export function GroupMultiplierBadge(props: {
  children?: ReactNode
  className?: string
  label?: string
  ratio?: number | null
}) {
  let colorClassName =
    'border-muted-foreground/30 bg-muted text-muted-foreground'
  if (props.ratio != null && props.ratio > 1) {
    colorClassName = 'border-warning/30 bg-warning/10 text-warning'
  } else if (props.ratio != null && props.ratio < 1) {
    colorClassName = 'border-info/30 bg-info/10 text-info'
  }

  return (
    <Badge
      variant='outline'
      className={cn(
        'relative h-5 min-w-12 rounded-full px-1.5 py-0 text-sm leading-none font-medium shadow-none',
        !props.label && 'tabular-nums',
        colorClassName,
        props.className
      )}
    >
      {props.children}
      <span>{props.label ?? `${props.ratio}x`}</span>
    </Badge>
  )
}

type GroupBadgeProps = Omit<
  StatusBadgeProps,
  'autoColor' | 'label' | 'variant'
> & {
  group?: string | null
  label?: string
  ratio?: number | null
  ratioLabel?: string
  containerClassName?: string
}

function getGroupLabel(params: {
  labelOverride?: string
  groupName?: string
  isAutoGroup: boolean
  isEmptyGroup: boolean
  t: (key: string) => string
}): string {
  if (params.labelOverride) return params.labelOverride
  if (params.isEmptyGroup) return params.t('User Group')
  if (params.isAutoGroup) return params.t('Auto')
  return params.groupName ?? ''
}

export function GroupBadge(props: GroupBadgeProps) {
  const { t } = useTranslation()
  const {
    group,
    label: labelOverride,
    ratio,
    ratioLabel,
    containerClassName,
    copyable = false,
    showDot,
    className,
    ...badgeProps
  } = props
  const groupName = group?.trim()
  const isAutoGroup = groupName === 'auto'
  const isEmptyGroup = !groupName
  const isSpecialGroup = isAutoGroup || isEmptyGroup
  const label = getGroupLabel({
    labelOverride,
    groupName,
    isAutoGroup,
    isEmptyGroup,
    t,
  })

  const badge = (
    <StatusBadge
      {...badgeProps}
      copyable={copyable}
      label={label}
      showDot={showDot ?? (isSpecialGroup ? false : undefined)}
      variant={isSpecialGroup ? 'neutral' : undefined}
      autoColor={isSpecialGroup ? undefined : groupName}
      className={cn('min-w-0 shrink overflow-hidden', className)}
    />
  )

  if (ratio == null && !ratioLabel) {
    return badge
  }

  return (
    <span
      className={cn(
        'inline-flex max-w-full min-w-0 items-center gap-2 text-sm',
        containerClassName
      )}
    >
      <span className='max-w-full min-w-0 overflow-hidden'>{badge}</span>
      <GroupMultiplierBadge ratio={ratio} label={ratioLabel} />
    </span>
  )
}
