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

import { CopyButton } from '@/components/copy-button'
import { Badge } from '@/components/ui/badge'

import { HOST_PROTOCOL_ENDPOINTS } from '../lib/host-protocols'
import type { TaskPluginMeta, TaskPluginRoute } from '../types'
import { PluginModelList } from './plugin-model-list'

/**
 * One HTTP endpoint the gateway serves for this plugin. Methods and paths are
 * wire vocabulary and stay raw; `children` carries the trailing annotations
 * (supported request forms, native route type) that belong to this endpoint.
 */
function EndpointRow(props: {
  method: string
  path: string
  children?: ReactNode
}) {
  const { t } = useTranslation()
  return (
    <li className='grid grid-cols-[3.5rem_minmax(0,1fr)_auto] items-start gap-2 py-2'>
      <Badge
        variant='outline'
        className='mt-1 justify-center font-mono font-normal'
      >
        {props.method}
      </Badge>
      <div className='min-w-0 space-y-1.5 pt-1'>
        <span className='block font-mono text-xs break-all'>{props.path}</span>
        <div className='flex flex-wrap items-center gap-1.5'>
          {props.children}
        </div>
      </div>
      <CopyButton
        value={props.path}
        className='size-7'
        iconClassName='size-3.5'
        aria-label={t('Copy endpoint path')}
      />
    </li>
  )
}

/**
 * The endpoints a plugin exposes: first the host protocol endpoints derived
 * from each `meta.protocols` claim, then the native routes it declares itself.
 *
 * The supported request forms of a mode-bearing protocol are rendered on the
 * create endpoint rather than next to the protocol name, because `supports`
 * gates exactly that call — retrieval of a created resource is always
 * available. Mode names are wire vocabulary and are never translated.
 */
export function PluginEndpoints(props: {
  title?: ReactNode
  protocolsNote?: ReactNode
  routesNote?: ReactNode
  protocols?: TaskPluginMeta['protocols']
  routes?: TaskPluginRoute[]
}) {
  const { t } = useTranslation()
  const claims = props.protocols ?? []
  const routes = props.routes ?? []

  return (
    <div className='space-y-3'>
      <h3 className='text-sm font-semibold'>{props.title ?? t('Endpoints')}</h3>
      {claims.length === 0 &&
      routes.length === 0 &&
      !props.protocolsNote &&
      !props.routesNote ? (
        <p className='text-muted-foreground text-sm'>{t('Not declared')}</p>
      ) : null}
      {props.protocolsNote}
      {claims.length > 0 && (
        <p className='text-xs font-medium'>
          {t('Standard protocol interfaces')}
        </p>
      )}
      {claims.map((claim) => {
        const name = typeof claim === 'string' ? claim : claim.name
        const supports = typeof claim === 'string' ? undefined : claim.supports
        const models = typeof claim === 'string' ? undefined : claim.models
        const endpoints = HOST_PROTOCOL_ENDPOINTS[name] ?? []
        const modeLabels = {
          stream: t('Streaming'),
          sync: t('Synchronous'),
          background: t('Background'),
        }
        const chips = supports?.map((mode) => (
          <Badge
            key={mode}
            variant='secondary'
            className='font-mono font-normal'
          >
            <span>{mode}</span>
            <span className='font-sans'>{modeLabels[mode]}</span>
          </Badge>
        ))
        // Chips belong on the create row, but a claim naming a protocol absent
        // from the frozen table has no rows at all; keep the declared forms
        // visible on the group header rather than dropping them silently.
        const hasCreateRow = endpoints.some((endpoint) => endpoint.modeBearing)
        return (
          <div key={name} className='bg-muted/30 space-y-1.5 rounded-md p-3'>
            <div className='flex flex-wrap items-center gap-x-2 gap-y-1'>
              <span className='text-muted-foreground min-w-0 font-mono text-xs break-all'>
                {name}
              </span>
              {models?.length ? (
                <PluginModelList
                  models={models}
                  collapsedLabel={t('Model scope')}
                />
              ) : null}
              {hasCreateRow ? null : chips}
            </div>
            <ul className='divide-y'>
              {endpoints.map((endpoint) => (
                <EndpointRow
                  key={`${endpoint.method} ${endpoint.path}`}
                  method={endpoint.method}
                  path={endpoint.path}
                >
                  <span className='text-muted-foreground text-xs'>
                    {endpoint.method === 'POST'
                      ? t('Submit request')
                      : t('Retrieve result')}
                  </span>
                  {endpoint.modeBearing ? chips : null}
                </EndpointRow>
              ))}
            </ul>
          </div>
        )
      })}
      {props.routesNote}
      {routes.length > 0 ? (
        <div className='bg-muted/30 space-y-1.5 rounded-md p-3'>
          <p className='text-muted-foreground text-xs'>
            {t('Plugin-defined interfaces')}
          </p>
          <ul className='divide-y'>
            {routes.map((route) => (
              <EndpointRow
                key={`${route.method} ${route.path}`}
                method={route.method}
                path={route.path}
              >
                <span className='text-muted-foreground font-mono text-[11px]'>
                  {route.type}
                </span>
                <span className='text-muted-foreground text-xs'>
                  {route.type === 'submit' && t('Submit request')}
                  {route.type === 'query' && t('Retrieve result')}
                  {route.type === 'dynamic' && t('Dynamic operation')}
                </span>
                {route.models?.length ? (
                  <PluginModelList
                    models={route.models}
                    collapsedLabel={t('Model scope')}
                  />
                ) : null}
              </EndpointRow>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}
