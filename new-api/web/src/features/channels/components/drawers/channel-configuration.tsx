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
import { Check, CircleAlert, CircleDashed } from 'lucide-react'
import { type ReactNode, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import type {
  ChannelConfigurationSection,
  ChannelConfigurationStatus,
} from '../../lib/channel-configuration'

type ChannelConfigurationProps = {
  section: ChannelConfigurationSection
  onSectionChange: (section: ChannelConfigurationSection) => void
  statuses: Record<ChannelConfigurationSection, ChannelConfigurationStatus>
  /** Item counts shown on a tab; a count replaces the plain configured mark. */
  badges?: Partial<
    Record<ChannelConfigurationSection, { count: number; label: string }>
  >
  connection: ReactNode
  models: ReactNode
  quickOptions: ReactNode
  routing: ReactNode
  request: ReactNode
  other: ReactNode
}

export function ChannelConfigurationStatusIndicator(props: {
  status: ChannelConfigurationStatus
  required?: boolean
}) {
  const { t } = useTranslation()
  if (props.status === 'idle' && !props.required) return null
  let label = t('Incomplete')
  let StatusIcon = CircleDashed
  let iconColor = 'text-warning'
  if (props.status === 'error') {
    label = t('Error')
    StatusIcon = CircleAlert
    iconColor = 'text-destructive'
  }
  if (props.status === 'ready') {
    label = t('Ready')
    StatusIcon = Check
    iconColor = 'text-success'
  }
  if (props.status === 'configured') {
    label = t('Configured')
    StatusIcon = Check
    iconColor = 'text-success'
  }
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <span
            role='img'
            aria-label={label}
            className={cn('inline-flex shrink-0 items-center', iconColor)}
          />
        }
      >
        <StatusIcon className='size-3.5' aria-hidden='true' />
      </TooltipTrigger>
      <TooltipContent>{label}</TooltipContent>
    </Tooltip>
  )
}

export function ChannelConfiguration(props: ChannelConfigurationProps) {
  const { t } = useTranslation()
  const [visited, setVisited] = useState(() => new Set([props.section]))
  useEffect(() => {
    setVisited((previous) => {
      if (previous.has(props.section)) return previous
      return new Set([...previous, props.section])
    })
  }, [props.section])
  const sections = [
    { id: 'connection', label: t('Connection & Models') },
    { id: 'routing', label: t('Routing & Mapping') },
    { id: 'request', label: t('Request & Response') },
    { id: 'other', label: t('Other Settings') },
  ] as const

  return (
    <Tabs
      value={props.section}
      onValueChange={(value) =>
        props.onSectionChange(value as ChannelConfigurationSection)
      }
      className='min-h-0 flex-1 gap-5'
    >
      <div className='-mt-1 shrink-0 overflow-x-auto py-1'>
        <TabsList
          aria-label={t('Channel configuration')}
          variant='line'
          className='min-w-full justify-start'
        >
          {sections.map((section) => {
            const status = props.statuses[section.id]
            const badge = props.badges?.[section.id]
            const showBadge = Boolean(badge && badge.count > 0)
            return (
              <TabsTrigger
                key={section.id}
                value={section.id}
                className='gap-2 px-3'
              >
                {section.label}
                {showBadge && badge && (
                  <Badge
                    variant='secondary'
                    aria-label={badge.label}
                    className='h-4 min-w-4 px-1 text-[10px] tabular-nums'
                  >
                    {badge.count}
                  </Badge>
                )}
                {!(showBadge && status === 'configured') && (
                  <ChannelConfigurationStatusIndicator
                    status={status}
                    required={section.id === 'connection'}
                  />
                )}
              </TabsTrigger>
            )
          })}
        </TabsList>
      </div>
      <TabsContent
        value='connection'
        keepMounted
        className='-m-1 min-h-0 overflow-y-auto overscroll-contain p-1'
      >
        <div className='grid min-w-0 items-start gap-6 lg:grid-cols-2'>
          <div className='flex min-w-0 flex-col gap-5'>{props.connection}</div>
          <div className='flex min-w-0 flex-col gap-5'>
            {props.models}
            {props.quickOptions}
          </div>
        </div>
      </TabsContent>
      <TabsContent
        value='routing'
        keepMounted
        className='-m-1 min-h-0 space-y-5 overflow-y-auto overscroll-contain p-1'
      >
        {(props.section === 'routing' || visited.has('routing')) &&
          props.routing}
      </TabsContent>
      <TabsContent
        value='request'
        keepMounted
        className='-m-1 min-h-0 space-y-5 overflow-y-auto overscroll-contain p-1'
      >
        {(props.section === 'request' || visited.has('request')) &&
          props.request}
      </TabsContent>
      <TabsContent
        value='other'
        keepMounted
        className='-m-1 min-h-0 space-y-5 overflow-y-auto overscroll-contain p-1'
      >
        {(props.section === 'other' || visited.has('other')) && props.other}
      </TabsContent>
    </Tabs>
  )
}
