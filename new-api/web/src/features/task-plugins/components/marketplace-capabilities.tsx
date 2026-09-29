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
import { useMemo, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { getChannelTypeLabel } from '@/features/channels/lib'

import {
  parsePluginMetaPreview,
  resolvePluginMetaPreview,
} from '../lib/plugin-meta-preview'
import type {
  MarketplaceIndexVersion,
  MarketplacePlugin,
  PluginPreviewField,
} from '../types'
import { PluginEndpoints } from './plugin-endpoints'
import { PluginModelList } from './plugin-model-list'

type MarketplaceCapabilitiesProps = {
  plugin: MarketplacePlugin
  version?: MarketplaceIndexVersion
  source?: string
  onRetry?: () => void
}

/** Make index fallbacks visible beside the fact they describe. */
function PreviewFact(props: {
  field: PluginPreviewField<unknown>
  children: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <div className='min-w-0 space-y-1.5'>
      {props.children}
      {props.field.state === 'value' && props.field.origin === 'index' && (
        <Badge
          variant='outline'
          className='text-muted-foreground text-[11px] font-normal'
        >
          {t('From marketplace index')}
        </Badge>
      )}
    </div>
  )
}

function EndpointPreviewNote(props: {
  field: PluginPreviewField<unknown[]>
  label: string
}) {
  const { t } = useTranslation()
  if (props.field.state === 'value' && props.field.value.length > 0) {
    return props.field.origin === 'index' ? (
      <p className='text-muted-foreground text-xs'>
        {props.label} · {t('From marketplace index')}
      </p>
    ) : null
  }
  let message = t('Information unavailable')
  if (props.field.state === 'missing' || props.field.state === 'value') {
    message = t('Not declared by this plugin')
  }
  return (
    <p className='text-muted-foreground text-xs'>
      {props.label}: {message}
    </p>
  )
}

export function MarketplaceCapabilities(props: MarketplaceCapabilitiesProps) {
  const { t } = useTranslation()
  const parsed = useMemo(
    () =>
      props.source === undefined
        ? undefined
        : parsePluginMetaPreview(props.source),
    [props.source]
  )
  const fields = resolvePluginMetaPreview(props.plugin, props.version, parsed)
  let channels = t('Information unavailable')
  if (fields.channelTypes.state === 'missing') channels = t('Task Plugin')
  if (fields.channelTypes.state === 'value') {
    channels = fields.channelTypes.value.length
      ? fields.channelTypes.value
          .map((type) => `${t(getChannelTypeLabel(type))} (#${type})`)
          .join(', ')
      : t('Task Plugin')
  }
  let hosts = t('Information unavailable')
  if (fields.allowedHosts.state === 'missing') {
    hosts = t('No extra domains declared')
  }
  if (fields.allowedHosts.state === 'value') {
    hosts =
      fields.allowedHosts.value.join(', ') || t('No extra domains declared')
  }
  let authentication = t('Information unavailable')
  if (fields.auth.state === 'missing') {
    authentication = t('Not declared; an API key may still be required.')
  }
  if (fields.auth.state === 'value') {
    authentication = fields.auth.value
    if (!authentication) {
      authentication = t('Not declared; an API key may still be required.')
    }
    if (authentication === 'api_key') authentication = t('API Key')
    if (authentication === 'none') {
      authentication = t('None (plugin declaration)')
    }
    if (authentication === 'oauth2_jwt' || authentication === 'vertex_oauth') {
      authentication = t('OAuth 2.0 service account')
    }
  }
  let baseUrl = t('Information unavailable')
  if (fields.baseUrl.state === 'missing') {
    baseUrl = t('Not declared by this plugin')
  }
  if (fields.baseUrl.state === 'value') {
    baseUrl = fields.baseUrl.value || t('Not declared by this plugin')
  }
  const connections = [
    {
      title: t('Applicable channels'),
      value: channels,
      field: fields.channelTypes,
      hint: t('Channel types you can use when binding this plugin.'),
    },
    {
      title: t('Upstream authentication'),
      value: authentication,
      field: fields.auth,
      hint: t(
        'Authentication for upstream requests; channel credentials may still be used.'
      ),
    },
    {
      title: t('Default API address'),
      value: baseUrl,
      field: fields.baseUrl,
      hint: t('Used when a Task Plugin channel has no API address configured.'),
    },
    {
      title: t('Additional request domains'),
      value: hosts,
      field: fields.allowedHosts,
      hint: t(
        'Extra destinations beyond the channel API address. The channel address remains accessible.'
      ),
    },
  ]

  return (
    <div className='min-w-0 space-y-5'>
      {parsed && parsed.status !== 'parsed' && (
        <ErrorState
          title={t('Some plugin information could not be read')}
          description={t(
            'Dynamic declarations cannot be previewed. Retry or review the source below.'
          )}
          onRetry={props.onRetry}
          className='min-h-0 border p-3'
        />
      )}
      <section className='min-w-0 space-y-3' aria-label={t('Supported models')}>
        <h3 className='text-sm font-medium'>{t('Supported models')}</h3>
        <PreviewFact field={fields.models}>
          {fields.models.state === 'value' && fields.models.value.length > 0 ? (
            <PluginModelList models={fields.models.value} />
          ) : (
            <p className='text-muted-foreground text-xs'>
              {fields.models.state === 'unknown'
                ? t('Information unavailable')
                : t('Not declared by this plugin')}
            </p>
          )}
        </PreviewFact>
      </section>
      <section
        className='min-w-0 rounded-lg border p-4'
        aria-label={t('Supported interfaces')}
      >
        <PluginEndpoints
          title={t('Supported interfaces')}
          protocols={
            fields.protocols.state === 'value'
              ? fields.protocols.value
              : undefined
          }
          routes={
            fields.routes.state === 'value' ? fields.routes.value : undefined
          }
          protocolsNote={
            <EndpointPreviewNote
              label={t('Standard protocol interfaces')}
              field={fields.protocols}
            />
          }
          routesNote={
            <EndpointPreviewNote
              label={t('Plugin-defined interfaces')}
              field={fields.routes}
            />
          }
        />
      </section>
      <section className='space-y-3' aria-label={t('Connection settings')}>
        <h3 className='text-sm font-medium'>{t('Connection settings')}</h3>
        <dl className='bg-muted/20 grid gap-x-6 gap-y-4 rounded-lg border p-4 sm:grid-cols-2'>
          {connections.map((connection, index) => (
            <div
              key={connection.title}
              className={
                index >= 2
                  ? 'min-w-0 space-y-1.5 sm:col-span-2'
                  : 'min-w-0 space-y-1.5'
              }
            >
              <dt className='text-muted-foreground text-xs'>
                {connection.title}
              </dt>
              <dd>
                <PreviewFact field={connection.field}>
                  <p className='text-xs leading-relaxed break-all'>
                    {connection.value}
                  </p>
                </PreviewFact>
                <p className='text-muted-foreground mt-1.5 text-xs leading-relaxed'>
                  {connection.hint}
                </p>
              </dd>
            </div>
          ))}
        </dl>
      </section>
      <p className='text-muted-foreground text-xs leading-relaxed'>
        {t(
          'This preview reads static plugin declarations. The gateway validates the plugin during installation.'
        )}
      </p>
    </div>
  )
}
