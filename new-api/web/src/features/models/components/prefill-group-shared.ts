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
import type { StatusBadgeProps } from '@/components/status-badge'

import { type PrefillGroup, type PrefillGroupFormValues } from '../types'

export type PrefillGroupType = PrefillGroup['type']

export const PREFILL_GROUP_TYPES = [
  {
    value: 'model' as PrefillGroupType,
    label: 'Model Group',
    description: 'Reusable sets of models you can attach to channels.',
    badge: 'blue' as StatusBadgeProps['variant'],
  },
  {
    value: 'tag' as PrefillGroupType,
    label: 'Tag Group',
    description: 'Collections of metadata tags for bulk operations.',
    badge: 'purple' as StatusBadgeProps['variant'],
  },
  {
    value: 'endpoint' as PrefillGroupType,
    label: 'Endpoint Group',
    description: 'HTTP endpoint mappings shared across providers.',
    badge: 'cyan' as StatusBadgeProps['variant'],
  },
] as const

export const PREFILL_GROUP_TYPE_META = PREFILL_GROUP_TYPES.reduce<
  Record<
    PrefillGroupType,
    { label: string; badge: StatusBadgeProps['variant'] }
  >
>(
  (acc, type) => {
    acc[type.value] = { label: type.label, badge: type.badge }
    return acc
  },
  {} as Record<
    PrefillGroupType,
    { label: string; badge: StatusBadgeProps['variant'] }
  >
)

export const DEFAULT_FORM_VALUES: PrefillGroupFormValues = {
  name: '',
  description: '',
  type: 'model',
  items: [],
}

export function parseStringItems(items: PrefillGroup['items']): string[] {
  if (!items) return []
  if (Array.isArray(items)) {
    return items
      .map((item) => (typeof item === 'string' ? item.trim() : ''))
      .filter(Boolean)
  }
  if (typeof items === 'string') {
    try {
      const parsed = JSON.parse(items)
      if (Array.isArray(parsed)) {
        return parsed
          .map((item) => (typeof item === 'string' ? item.trim() : ''))
          .filter(Boolean)
      }
    } catch {
      return items
        .split(/[\n,]/)
        .map((item) => item.trim())
        .filter(Boolean)
    }
  }
  return []
}

export function parseEndpointKeys(items: PrefillGroup['items']): string[] {
  if (!items) return []
  try {
    const parsed =
      typeof items === 'string' ? JSON.parse(items || '{}') : (items as unknown)
    if (Array.isArray(parsed)) {
      return parsed
        .map((item) =>
          typeof item === 'string'
            ? item
            : typeof item?.name === 'string'
              ? item.name
              : ''
        )
        .filter(Boolean)
    }
    if (parsed && typeof parsed === 'object') {
      return Object.keys(parsed)
    }
  } catch {
    return []
  }
  return []
}

export function serializeEndpointItems(items: PrefillGroup['items']): string {
  if (!items) return ''
  if (typeof items === 'string') {
    return items
  }
  try {
    return JSON.stringify(items, null, 2)
  } catch {
    return ''
  }
}
