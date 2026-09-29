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

/**
 * Generic status mapping utility
 * Creates a function to map status values to labels and variants
 */
export function createStatusMapper<T extends string>(mapping: {
  [key in T]?: { label: string; variant: StatusBadgeProps['variant'] }
}) {
  return {
    getLabel: (status: string, defaultLabel = 'Unknown'): string => {
      return mapping[status as T]?.label ?? defaultLabel
    },
    getVariant: (
      status: string,
      defaultVariant: StatusBadgeProps['variant'] = 'neutral'
    ): StatusBadgeProps['variant'] => {
      return mapping[status as T]?.variant ?? defaultVariant
    },
  }
}
