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
import { Activity, RotateCw } from 'lucide-react'
import { memo, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { IconBadge } from '@/components/ui/icon-badge'
import { ScrollArea } from '@/components/ui/scroll-area'
import { getUptimeStatus } from '@/features/dashboard/api'
import type {
  UptimeGroupResult,
  UptimeMonitor,
} from '@/features/dashboard/types'
import { cn } from '@/lib/utils'

import { PanelWrapper } from '../ui/panel-wrapper'

const STATUS_COLOR_MAP: Record<number, string> = {
  1: 'bg-emerald-500',
  0: 'bg-red-500',
  2: 'bg-amber-500',
  3: 'bg-blue-500',
}
const DEFAULT_STATUS_COLOR = 'bg-muted-foreground/40'

const StatusDot = memo(function StatusDot(props: { status: number }) {
  const color = STATUS_COLOR_MAP[props.status] ?? DEFAULT_STATUS_COLOR
  return <span className={cn('inline-block size-2 rounded-full', color)} />
})

export function UptimePanel() {
  const { t } = useTranslation()
  const [groups, setGroups] = useState<UptimeGroupResult[]>([])
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)

  useEffect(() => {
    const abortController = new AbortController()

    void getUptimeStatus()
      .then((res) => {
        if (abortController.signal.aborted) return
        setGroups(res?.data || [])
      })
      .catch(() => {
        if (abortController.signal.aborted) return
        setGroups([])
      })
      .finally(() => {
        if (!abortController.signal.aborted) {
          setLoading(false)
        }
      })

    return () => {
      abortController.abort()
    }
  }, [])

  const handleRefresh = () => {
    const abortController = new AbortController()
    setRefreshing(true)

    void getUptimeStatus()
      .then((res) => {
        if (abortController.signal.aborted) return
        setGroups(res?.data || [])
      })
      .catch(() => {
        if (abortController.signal.aborted) return
        setGroups([])
      })
      .finally(() => {
        if (!abortController.signal.aborted) {
          setRefreshing(false)
        }
      })
  }

  return (
    <PanelWrapper
      title={
        <span className='flex items-center gap-2'>
          <IconBadge tone='success' size='sm'>
            <Activity />
          </IconBadge>
          {t('Uptime')}
        </span>
      }
      description={t('Grouped monitor status from Uptime Kuma')}
      loading={loading}
      empty={!groups.length}
      emptyMessage={t('No uptime monitoring configured')}
      height='h-80'
      contentClassName='p-0'
      headerActions={
        <Button
          variant='ghost'
          size='sm'
          onClick={handleRefresh}
          disabled={refreshing}
          className='size-7 p-0'
        >
          <RotateCw
            className={cn('size-3.5', refreshing && 'animate-spin')}
            aria-label={t('Refresh')}
          />
        </Button>
      }
    >
      <ScrollArea className='h-80'>
        <div>
          {groups.map((group, groupIdx) => (
            <div key={group.categoryName}>
              <div className='bg-muted/30 border-border/60 border-b px-3 py-2 sm:px-5'>
                <div className='flex items-center gap-2'>
                  <h4 className='text-muted-foreground text-xs font-semibold tracking-wider uppercase'>
                    {group.categoryName}
                  </h4>
                  <span className='text-muted-foreground/40 font-mono text-xs tabular-nums'>
                    {group.monitors?.length || 0}
                  </span>
                </div>
              </div>

              {group.monitors?.map(
                (monitor: UptimeMonitor, monitorIdx: number) => (
                  <div
                    key={monitor.name}
                    className={cn(
                      'hover:bg-muted/40 flex items-center justify-between gap-2 px-3 py-2 transition-colors sm:px-5 sm:py-2.5',
                      monitorIdx < (group.monitors?.length || 0) - 1 &&
                        'border-border/40 border-b',
                      groupIdx < groups.length - 1 &&
                        monitorIdx === (group.monitors?.length || 0) - 1 &&
                        'border-border/60 border-b'
                    )}
                  >
                    <div className='flex min-w-0 items-center gap-2.5'>
                      <StatusDot status={monitor.status} />
                      <span className='truncate text-sm'>{monitor.name}</span>
                      {monitor.group && (
                        <span className='text-muted-foreground/40 shrink-0 text-xs'>
                          ({monitor.group})
                        </span>
                      )}
                    </div>
                    <span className='text-foreground shrink-0 font-mono text-sm font-semibold tabular-nums'>
                      {((monitor.uptime ?? 0) * 100).toFixed(2)}%
                    </span>
                  </div>
                )
              )}
            </div>
          ))}
        </div>
      </ScrollArea>
    </PanelWrapper>
  )
}
