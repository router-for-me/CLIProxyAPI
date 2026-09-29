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
import type { Table } from '@tanstack/react-table'
import { X } from 'lucide-react'
import { useState, useEffect, useLayoutEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

type DataTableBulkActionsProps<TData> = {
  table: Table<TData>
  placement?: 'floating' | 'inline'
  entityName: string
  children: React.ReactNode
}

/**
 * A modular toolbar for displaying bulk actions when table rows are selected.
 *
 * @template TData The type of data in the table.
 * @param {object} props The component props.
 * @param {Table<TData>} props.table The react-table instance.
 * @param {string} props.entityName The name of the entity being acted upon (e.g., "task", "user").
 * @param {React.ReactNode} props.children The action buttons to be rendered inside the toolbar.
 * @returns {React.ReactNode | null} The rendered component or null if no rows are selected.
 */
export function DataTableBulkActions<TData>({
  table,
  entityName,
  placement = 'floating',
  children,
}: DataTableBulkActionsProps<TData>): React.ReactNode | null {
  const { t } = useTranslation()
  const selectedRows = table.getFilteredSelectedRowModel().rows
  const selectedCount = selectedRows.length
  const toolbarRef = useRef<HTMLDivElement>(null)
  const buttonsRef = useRef<HTMLButtonElement[]>([])
  const [announcement, setAnnouncement] = useState('')

  useLayoutEffect(() => {
    buttonsRef.current = toolbarRef.current
      ? [...toolbarRef.current.querySelectorAll('button')]
      : []
  })

  // Announce selection changes to screen readers
  useEffect(() => {
    if (selectedCount > 0) {
      const message = t(
        '{{count}} records selected. Bulk actions are available.',
        { count: selectedCount }
      )
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setAnnouncement(message)

      // Clear announcement after a delay
      const timer = setTimeout(() => setAnnouncement(''), 3000)
      return () => clearTimeout(timer)
    }
  }, [selectedCount, entityName, t])

  const handleClearSelection = () => {
    table.resetRowSelection()
  }

  const handleKeyDown = (event: React.KeyboardEvent) => {
    const buttons = buttonsRef.current
    if (buttons.length === 0) return

    const currentIndex = buttons.indexOf(
      document.activeElement as HTMLButtonElement
    )

    switch (event.key) {
      case 'ArrowRight': {
        event.preventDefault()
        const nextIndex = (currentIndex + 1) % buttons.length
        buttons[nextIndex]?.focus()
        break
      }
      case 'ArrowLeft': {
        event.preventDefault()
        const prevIndex =
          currentIndex === 0 ? buttons.length - 1 : currentIndex - 1
        buttons[prevIndex]?.focus()
        break
      }
      case 'Home':
        event.preventDefault()
        buttons[0]?.focus()
        break
      case 'End':
        event.preventDefault()
        buttons.at(-1)?.focus()
        break
      case 'Escape': {
        // Check if the Escape key came from a dropdown trigger or content
        // We can't check dropdown state because the menu closes before our handler runs.
        const target = event.target as HTMLElement
        const activeElement = document.activeElement as HTMLElement

        // Check if the event target or currently focused element is a dropdown trigger
        const isFromDropdownTrigger =
          target?.getAttribute('data-slot') === 'dropdown-menu-trigger' ||
          activeElement?.getAttribute('data-slot') ===
            'dropdown-menu-trigger' ||
          target?.closest('[data-slot="dropdown-menu-trigger"]') ||
          activeElement?.closest('[data-slot="dropdown-menu-trigger"]')

        // Check if the focused element is inside dropdown content (which is portaled)
        const isFromDropdownContent =
          activeElement?.closest('[data-slot="dropdown-menu-content"]') ||
          target?.closest('[data-slot="dropdown-menu-content"]')

        if (isFromDropdownTrigger || isFromDropdownContent) {
          // Escape was meant for the dropdown - don't clear selection
          return
        }

        // Escape was meant for the toolbar - clear selection
        event.preventDefault()
        handleClearSelection()
        break
      }
    }
  }

  if (selectedCount === 0) {
    return null
  }

  return (
    <>
      {/* Live region for screen reader announcements */}
      <div
        aria-live='polite'
        aria-atomic='true'
        className='sr-only'
        role='status'
      >
        {announcement}
      </div>

      <div
        ref={toolbarRef}
        role='toolbar'
        aria-label={t('Bulk actions for {{count}} selected records', {
          count: selectedCount,
        })}
        aria-describedby='bulk-actions-description'
        tabIndex={-1}
        onKeyDown={handleKeyDown}
        className={cn(
          placement === 'floating'
            ? 'fixed bottom-6 left-1/2 z-50 w-max max-w-[calc(100vw-2rem)] -translate-x-1/2 rounded-xl transition-all delay-100 duration-300 ease-out hover:scale-105'
            : 'shrink-0 rounded-xl',
          'focus-visible:ring-ring/50 focus-visible:ring-2 focus-visible:outline-none'
        )}
      >
        <div
          className={cn(
            'p-2 shadow-xl',
            'rounded-xl border',
            'bg-background/95 supports-[backdrop-filter]:bg-background/60 backdrop-blur-lg',
            'flex flex-wrap items-center gap-2'
          )}
        >
          <Tooltip>
            <TooltipTrigger
              render={
                <Button
                  variant='outline'
                  size='icon'
                  onClick={handleClearSelection}
                  className='size-6'
                  aria-label={t('Clear selection')}
                  title={t('Clear selection (Escape)')}
                />
              }
            >
              <X />
              <span className='sr-only'>{t('Clear selection')}</span>
            </TooltipTrigger>
            <TooltipContent>
              <p>{t('Clear selection (Escape)')}</p>
            </TooltipContent>
          </Tooltip>

          <Separator
            className='h-5'
            orientation='vertical'
            aria-hidden='true'
          />

          <div
            className='flex items-center gap-x-1 text-sm'
            id='bulk-actions-description'
          >
            <Badge
              variant='default'
              className='min-w-8 rounded-lg'
              aria-label={t('{{count}} selected', { count: selectedCount })}
            >
              {selectedCount}
            </Badge>{' '}
            <span className='hidden sm:inline'>
              {entityName}
              {selectedCount > 1 && /^[a-z]+$/i.test(entityName) ? 's' : ''}
            </span>{' '}
            {t('selected')}
          </div>

          <Separator
            className='h-5'
            orientation='vertical'
            aria-hidden='true'
          />

          {children}
        </div>
      </div>
    </>
  )
}
