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
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { Badge } from '@/components/ui/badge'
import { getChannelTypeLabel } from '@/features/channels/lib/channel-utils'

import { getPluginWebsite } from '../lib/plugin-website'
import type { TaskPluginMeta } from '../types'
import { PluginEndpoints } from './plugin-endpoints'
import { PluginModelList } from './plugin-model-list'
import { PluginWebsiteLink } from './plugin-website-link'

type PluginMetadataCardProps = { meta: TaskPluginMeta }

export function PluginMetadataCard(props: PluginMetadataCardProps) {
  const { t } = useTranslation()
  const models = props.meta.models ?? []
  const fields = [
    { label: t('Sort priority'), value: String(props.meta.sortPriority ?? 0) },
    { label: t('Fetch mode'), value: props.meta.fetchMode },
    {
      label: t('Channel types'),
      value: props.meta.channelTypes?.length
        ? props.meta.channelTypes
            .map((type) => `${t(getChannelTypeLabel(type))} (#${type})`)
            .join(', ')
        : t('Task Plugin'),
    },
  ]
  let canCopyBaseUrl = false
  try {
    const url = new URL(props.meta.baseUrl ?? '')
    canCopyBaseUrl = url.protocol === 'https:' || url.protocol === 'http:'
  } catch {
    /* A missing or invalid URL is displayed verbatim without a copy action. */
  }

  return (
    <div className='min-w-0 space-y-4'>
      <section
        className='rounded-lg border p-4'
        aria-label={t('Plugin metadata')}
      >
        <h3 className='mb-4 text-sm font-semibold'>{t('Plugin metadata')}</h3>
        <dl className='grid gap-x-6 gap-y-4 sm:grid-cols-2'>
          {fields.map((field) => (
            <div key={field.label} className='min-w-0 space-y-1'>
              <dt className='text-muted-foreground text-xs'>{field.label}</dt>
              <dd className='text-sm break-words'>
                {field.value || t('Not declared')}
              </dd>
            </div>
          ))}
          <div className='min-w-0 space-y-1'>
            <dt className='text-muted-foreground text-xs'>
              {t('Plugin website')}
            </dt>
            <dd>
              {getPluginWebsite(props.meta.website) ? (
                <PluginWebsiteLink website={props.meta.website} />
              ) : (
                <span className='text-sm'>{t('Not declared')}</span>
              )}
            </dd>
          </div>
          <div className='min-w-0 space-y-1 sm:col-span-2'>
            <dt className='text-muted-foreground text-xs'>
              {t('Default base URL')}
            </dt>
            <dd className='flex items-start gap-2'>
              <span className='min-w-0 flex-1 pt-1 font-mono text-xs break-all'>
                {props.meta.baseUrl || t('Not declared')}
              </span>
              {canCopyBaseUrl && (
                <CopyButton
                  value={props.meta.baseUrl ?? ''}
                  className='size-7'
                  iconClassName='size-3.5'
                  aria-label={t('Copy base URL')}
                />
              )}
            </dd>
          </div>
        </dl>
      </section>
      <section className='rounded-lg border p-4' aria-label={t('Models')}>
        <div className='mb-3 flex items-center gap-2'>
          <h3 className='text-sm font-semibold'>{t('Models')}</h3>
          <Badge variant='secondary'>{models.length}</Badge>
          {models.length > 0 && (
            <CopyButton
              value={models.join('\n')}
              className='ml-auto size-7'
              iconClassName='size-3.5'
              aria-label={t('Copy all models')}
            />
          )}
        </div>
        {models.length ? (
          <PluginModelList models={models} maxVisible={models.length} />
        ) : (
          <p className='text-muted-foreground text-sm'>{t('Not declared')}</p>
        )}
      </section>
      <section className='rounded-lg border p-4' aria-label={t('Endpoints')}>
        <PluginEndpoints
          protocols={props.meta.protocols}
          routes={props.meta.routes}
        />
      </section>
    </div>
  )
}
