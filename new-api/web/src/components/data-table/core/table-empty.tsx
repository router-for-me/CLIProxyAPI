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
import { Database } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { TableRow, TableCell } from '@/components/ui/table'

interface TableEmptyProps {
  /**
   * Number of columns to span
   */
  colSpan: number
  /**
   * Custom title for empty state
   * @default 'No Data'
   */
  title?: string
  /**
   * Custom description for empty state
   * @default 'No records found. Try adjusting your filters.'
   */
  description?: string
  /**
   * Custom icon component
   * @default Database icon
   */
  icon?: React.ReactNode
  /**
   * Additional content to display (e.g., buttons)
   */
  children?: React.ReactNode
}

/**
 * Generic table empty state component
 * Displays a centered empty state message when table has no data
 */
export function TableEmpty({
  colSpan,
  title,
  description,
  icon,
  children,
}: TableEmptyProps) {
  const { t } = useTranslation()
  const resolvedTitle = title ?? t('No Data')
  const resolvedDescription =
    description ?? t('No records found. Try adjusting your filters.')
  return (
    <TableRow>
      <TableCell colSpan={colSpan} className='h-[400px] p-0'>
        <Empty>
          <EmptyHeader>
            <EmptyMedia variant='icon'>
              {icon || <Database className='size-6' />}
            </EmptyMedia>
            <EmptyTitle>{resolvedTitle}</EmptyTitle>
            <EmptyDescription>{resolvedDescription}</EmptyDescription>
          </EmptyHeader>
          {children}
        </Empty>
      </TableCell>
    </TableRow>
  )
}
