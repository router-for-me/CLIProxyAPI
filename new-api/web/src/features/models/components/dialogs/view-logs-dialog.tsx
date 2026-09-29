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
import { useQuery } from '@tanstack/react-query'
import { Download, Loader2, RefreshCcw, Terminal } from 'lucide-react'
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
import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { IconBadge } from '@/components/ui/icon-badge'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getDeploymentLogs, listDeploymentContainers } from '../../api'

interface ViewLogsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  deploymentId: string | number | null
}

export function ViewLogsDialog({
  open,
  onOpenChange,
  deploymentId,
}: ViewLogsDialogProps) {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const [autoScroll, setAutoScroll] = useState(true)
  const [autoRefresh, setAutoRefresh] = useState(false)
  const [stream, setStream] = useState<'stdout' | 'stderr' | 'all'>('stdout')
  const [containerId, setContainerId] = useState<string>('')

  const {
    data: containersData,
    isLoading: isLoadingContainers,
    refetch: refetchContainers,
    isFetching: isFetchingContainers,
  } = useQuery({
    queryKey: ['deployment-containers', deploymentId],
    queryFn: async () =>
      requireServerSuccess(
        await (deploymentId ? listDeploymentContainers(deploymentId) : null)
      ),
    enabled: open && deploymentId !== null,
  })

  const containers = useMemo(() => {
    const items = containersData?.data?.containers
    return Array.isArray(items) ? items : []
  }, [containersData?.data?.containers])

  useEffect(() => {
    if (!open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setContainerId('')

      setStream('stdout')

      setAutoScroll(true)

      setAutoRefresh(false)
      return
    }

    if (open && containers.length > 0 && !containerId) {
      const first = containers[0]?.container_id
      if (typeof first === 'string' && first) {
        setContainerId(first)
      }
    }
  }, [open, containers, containerId])

  const {
    data: logsData,
    isLoading: isLoadingLogs,
    refetch: refetchLogs,
    isFetching: isFetchingLogs,
  } = useQuery({
    queryKey: ['deployment-logs', deploymentId, containerId, stream],
    queryFn: async () =>
      requireServerSuccess(
        await (deploymentId && containerId
          ? getDeploymentLogs(deploymentId, {
              container_id: containerId,
              stream,
              limit: 500,
            })
          : null)
      ),
    enabled: open && deploymentId !== null && Boolean(containerId),
    refetchInterval: open && autoRefresh ? 5000 : false,
  })

  const logsText = useMemo(() => {
    const raw = logsData?.data
    return typeof raw === 'string' ? raw : ''
  }, [logsData?.data])

  const logLines = useMemo(() => {
    const normalized = logsText.replaceAll(/\r\n?/g, '\n')
    return normalized ? normalized.split('\n') : []
  }, [logsText])
  const keyedLogLines = useMemo(() => {
    const seen = new Map<string, number>()
    return logLines.map((line) => {
      const count = seen.get(line) ?? 0
      seen.set(line, count + 1)
      return { key: `${line}-${count}`, line }
    })
  }, [logLines])

  // Auto-scroll to bottom
  useEffect(() => {
    if (autoScroll && scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight
    }
  }, [logLines, autoScroll])

  const handleDownload = () => {
    if (!logsText.trim()) return
    const blob = new Blob([logsText], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = `deployment-${deploymentId}-${containerId || 'logs'}.txt`
    a.click()
    URL.revokeObjectURL(url)
  }
  let containerPlaceholder = t('Select')
  if (isLoadingContainers) {
    containerPlaceholder = t('Loading...')
  } else if (containers.length === 0) {
    containerPlaceholder = t('No containers')
  }
  let logsContent: ReactNode
  if (isLoadingContainers || isLoadingLogs) {
    logsContent = (
      <div className='flex items-center justify-center py-8'>
        <Loader2 className='h-6 w-6 animate-spin text-gray-400' />
      </div>
    )
  } else if (containers.length === 0) {
    logsContent = (
      <div className='py-8 text-center text-gray-400'>{t('No containers')}</div>
    )
  } else if (!containerId) {
    logsContent = (
      <div className='py-8 text-center text-gray-400'>
        {t('Please select a container')}
      </div>
    )
  } else if (!logsText.trim()) {
    logsContent = (
      <div className='py-8 text-center text-gray-400'>{t('No logs')}</div>
    )
  } else {
    logsContent = (
      <div className='font-mono text-sm'>
        {keyedLogLines.map(({ key, line }) => (
          <div key={key} className='whitespace-pre-wrap text-gray-200'>
            {line}
          </div>
        ))}
      </div>
    )
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={
        <>
          <IconBadge tone='chart-3' size='sm'>
            <Terminal />
          </IconBadge>
          {t('Deployment logs')}
        </>
      }
      contentClassName='flex h-[calc(100dvh-2rem)] flex-col max-sm:w-screen max-sm:max-w-none max-sm:rounded-none max-sm:p-4 sm:h-[80vh] sm:max-w-4xl'
      titleClassName='flex items-center gap-2'
      contentHeight='min(72vh, 720px)'
      bodyClassName='space-y-4'
    >
      <div className='mb-3 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between sm:gap-3'>
        <div className='text-muted-foreground text-sm'>
          {t('Deployment ID')}: {deploymentId}
        </div>
        <div className='grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center'>
          <Button
            variant='outline'
            size='sm'
            onClick={() => {
              refetchContainers()
              refetchLogs()
            }}
            disabled={isFetchingLogs || isFetchingContainers}
          >
            {isFetchingLogs || isFetchingContainers ? (
              <Loader2 className='mr-2 h-4 w-4 animate-spin' />
            ) : (
              <RefreshCcw className='mr-2 h-4 w-4' />
            )}
            {t('Refresh')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            onClick={handleDownload}
            disabled={!logsText.trim()}
          >
            <Download className='mr-2 h-4 w-4' />
            {t('Download')}
          </Button>
          <div className='col-span-2 flex items-center justify-between gap-2 rounded-md border px-3 py-1.5 sm:col-span-1'>
            <span className='text-xs'>{t('Auto refresh')}</span>
            <Switch checked={autoRefresh} onCheckedChange={setAutoRefresh} />
          </div>
        </div>
      </div>
      <div className='mb-3 grid gap-2 sm:grid-cols-2 sm:gap-3'>
        <div className='space-y-1'>
          <div className='text-muted-foreground text-xs'>{t('Container')}</div>
          <Combobox
            options={containers.flatMap((c) => {
              const id = c?.container_id
              if (typeof id !== 'string' || !id) return []
              const status =
                typeof c?.status === 'string' && c.status
                  ? ` (${c.status})`
                  : ''
              return [
                {
                  value: id,
                  label: `${id}${status}`,
                },
              ]
            })}
            value={containerId}
            onValueChange={(v) => v !== null && setContainerId(v)}
            disabled={isLoadingContainers || containers.length === 0}
            className='w-full'
            placeholder={containerPlaceholder}
          />
        </div>
        <div className='space-y-1'>
          <div className='text-muted-foreground text-xs'>{t('Stream')}</div>
          <Select
            items={[
              { value: 'stdout', label: 'stdout' },
              { value: 'stderr', label: 'stderr' },
              { value: 'all', label: 'all' },
            ]}
            value={stream}
            onValueChange={(v) => {
              if (v === 'stderr' || v === 'all' || v === 'stdout') {
                setStream(v)
              } else {
                setStream('stdout')
              }
            }}
          >
            <SelectTrigger>
              <SelectValue placeholder={t('Select')} />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              <SelectGroup>
                <SelectItem value='stdout'>stdout</SelectItem>
                <SelectItem value='stderr'>stderr</SelectItem>
                <SelectItem value='all'>all</SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>
      </div>
      <div
        ref={scrollRef}
        className='bg-muted flex-1 overflow-auto rounded-md border p-3 sm:p-4'
        onScroll={(e) => {
          const target = e.target as HTMLDivElement
          const isAtBottom =
            target.scrollHeight - target.scrollTop - target.clientHeight < 50
          setAutoScroll(isAtBottom)
        }}
      >
        {logsContent}
      </div>
    </Dialog>
  )
}
