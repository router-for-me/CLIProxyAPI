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
import { ChevronRight } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { PluginIcon } from '@/features/task-plugins/components/plugin-icon'
import { resolveLocalizedText } from '@/lib/localized-text'

import type { TaskPluginOption } from '../api'

export function ChannelPluginExtensions(props: {
  plugins: TaskPluginOption[]
  selected: string[]
  onConfigure: (pluginKey: string) => void
}) {
  const { t, i18n } = useTranslation()
  const plugins = props.plugins.filter((plugin) => plugin.models.length > 0)
  if (plugins.length === 0) return null
  const selected = new Set(props.selected)

  return (
    <div
      role='group'
      aria-label={t('Plugin extensions')}
      className='flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1'
    >
      <span className='text-muted-foreground text-xs'>
        {t('Plugin extensions')}
      </span>
      {plugins.map((plugin) => {
        const models = [...new Set(plugin.models)]
        const selectedCount = models.filter((model) =>
          selected.has(model)
        ).length
        const selectionLabel = t('Selected {{selected}} / {{total}}', {
          selected: selectedCount,
          total: models.length,
        })
        const description = resolveLocalizedText(
          plugin.description,
          i18n.language
        )
        return (
          <Button
            key={plugin.key}
            type='button'
            variant='ghost'
            size='sm'
            className='max-w-full min-w-0 gap-1.5 px-1.5 font-normal'
            aria-haspopup='dialog'
            aria-label={`${plugin.name} ${selectionLabel}`}
            title={description ? `${plugin.name}: ${description}` : plugin.name}
            onClick={() => props.onConfigure(plugin.key)}
          >
            <span aria-hidden='true' className='shrink-0'>
              <PluginIcon plugin={plugin} size={16} />
            </span>
            <span className='max-w-36 truncate'>{plugin.name}</span>
            <span className='text-muted-foreground shrink-0 text-xs tabular-nums'>
              {selectionLabel}
            </span>
            <ChevronRight
              className='text-muted-foreground size-3 shrink-0'
              aria-hidden='true'
            />
          </Button>
        )
      })}
    </div>
  )
}
