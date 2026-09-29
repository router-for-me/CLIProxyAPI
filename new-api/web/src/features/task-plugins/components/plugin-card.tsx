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
import { flexRender, type Row } from '@tanstack/react-table'
import { memo } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { resolveLocalizedText } from '@/lib/localized-text'

import type { TaskPluginListItem } from '../types'
import { PluginIcon } from './plugin-icon'
import { PluginWebsiteLink } from './plugin-website-link'

/**
 * A card is one grid cell, so the model list has to stay a fixed number of
 * lines regardless of how many models a plugin binds. The overflow count keeps
 * the remaining names reachable through its tooltip.
 */
const MAX_VISIBLE_MODELS = 4

/**
 * Bespoke task-plugin card for the card view. Reuses the column cell renderers
 * via `flexRender` so the table and card views share one implementation of the
 * source/runtime badges, the enable switch (with its usage-guard mutation),
 * and the actions menu.
 *
 * The card answers "which plugin is this and is it live" — identity, source,
 * runtime state, the plugin version, the models it binds, and the enable toggle.
 * Manifest detail (billing parameters, endpoints, source) belongs to the detail
 * sheet: rendering it here made every card a different height and buried the
 * plugin's own description under its parameter descriptions.
 */
function PluginCardComponent({ row }: { row: Row<TaskPluginListItem> }) {
  const { t, i18n } = useTranslation()
  const cells = row.getAllCells()
  const description = resolveLocalizedText(
    row.original.meta.description,
    i18n.language
  )
  const models = row.original.meta.models ?? []
  const hiddenModels = models.slice(MAX_VISIBLE_MODELS)

  const renderCell = (id: string) => {
    const cell = cells.find((c) => c.column.id === id)
    if (!cell || !cell.column.columnDef.cell) {
      return null
    }
    return flexRender(cell.column.columnDef.cell, cell.getContext())
  }

  const labelClass = 'text-muted-foreground text-[11px] font-medium select-none'

  return (
    <div className='flex h-full flex-col gap-2'>
      {/* Row 1: type icon + name/key, with runtime status + actions menu */}
      <div className='flex items-start justify-between gap-2'>
        <div className='flex min-w-0 flex-1 items-center gap-2.5'>
          <span className='mt-0.5 shrink-0'>
            <PluginIcon
              plugin={{ ...row.original.meta, hasIcon: row.original.has_icon }}
              size={20}
            />
          </span>
          <div className='min-w-0'>
            <div className='truncate text-sm font-medium'>
              {row.original.meta.name}
            </div>
            <div className='text-muted-foreground truncate font-mono text-xs'>
              {row.original.meta.key}
            </div>
          </div>
        </div>
        <div className='flex shrink-0 items-center gap-1.5'>
          {renderCell('actions')}
        </div>
      </div>

      <PluginWebsiteLink website={row.original.meta.website} />

      {/* Row 2: source, runtime, and plugin version badges wrap freely. */}
      <div className='flex flex-wrap items-center gap-1.5'>
        {renderCell('source')}
        {renderCell('runtime')}
        <Badge
          variant='secondary'
          className='font-mono font-normal'
          aria-label={`${t('Active version')} ${row.original.meta.version}`}
        >
          {row.original.meta.version ? `v${row.original.meta.version}` : '—'}
        </Badge>
      </div>

      {description ? (
        <p className='text-muted-foreground line-clamp-2 text-xs'>
          {description}
        </p>
      ) : null}

      {/* Row 3: the bound models, named rather than counted */}
      {models.length > 0 ? (
        <div className='space-y-1.5'>
          <div className={labelClass}>{t('Models')}</div>
          <div className='flex flex-wrap gap-1'>
            {models.slice(0, MAX_VISIBLE_MODELS).map((model) => (
              <Badge
                key={model}
                variant='outline'
                className='max-w-full font-mono font-normal'
                title={model}
              >
                <span className='min-w-0 truncate'>{model}</span>
              </Badge>
            ))}
            {hiddenModels.length > 0 ? (
              <Badge
                variant='secondary'
                className='font-normal'
                title={hiddenModels.join(', ')}
              >
                +{hiddenModels.length}
              </Badge>
            ) : null}
          </div>
        </div>
      ) : null}

      {/* Footer: enabled toggle pinned to the card bottom */}
      <div className='mt-auto flex items-center justify-between gap-2 border-t pt-2'>
        <span className={labelClass}>{t('Enabled')}</span>
        {renderCell('enabled')}
      </div>
    </div>
  )
}

/**
 * Memoized so each card only re-renders when its own react-table row reference
 * changes rather than on every parent table state update.
 */
export const PluginCard = memo(PluginCardComponent)
