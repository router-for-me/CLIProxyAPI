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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  AlertTriangle,
  Loader2,
  RefreshCw,
  ServerCog,
  Trash2,
} from 'lucide-react'
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { ErrorState } from '@/components/error-state'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Popover,
  PopoverContent,
  PopoverDescription,
  PopoverHeader,
  PopoverTitle,
  PopoverTrigger,
} from '@/components/ui/popover'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatTimestampRelative, formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import {
  deleteStaleSystemInstance,
  deleteStaleSystemInstances,
  listSystemInstances,
} from '../api'
import type { SystemInstance, SystemInstanceStatus } from '../types'

const INSTANCE_POLL_INTERVAL_MS = 30_000
const INSTANCE_SKELETON_KEYS = [
  'system-instance-skeleton-1',
  'system-instance-skeleton-2',
  'system-instance-skeleton-3',
]

const STATUS_CLASS_NAME: Record<SystemInstanceStatus, string> = {
  online:
    'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300',
  stale: 'bg-amber-50 text-amber-700 dark:bg-amber-500/15 dark:text-amber-300',
}

const STATUS_DOT_CLASS_NAME: Record<SystemInstanceStatus, string> = {
  online: 'bg-emerald-500',
  stale: 'bg-amber-500',
}

function roleLabel(instance: SystemInstance) {
  if (instance.info?.role?.is_master) return 'master'
  return 'worker'
}

function roleDescriptionKey(instance: SystemInstance) {
  if (instance.info?.role?.is_master) {
    return 'Master instances run scheduled background tasks.'
  }
  return 'Worker instances do not run master-only background tasks.'
}

function runtimeLabel(instance: SystemInstance) {
  const runtime = instance.info?.runtime
  if (!runtime?.goos && !runtime?.goarch) return '-'

  const parts: string[] = []
  if (runtime.goos || runtime.goarch) {
    parts.push([runtime.goos, runtime.goarch].filter(Boolean).join('/'))
  }
  return parts.join(' · ')
}

function getNodeName(instance: SystemInstance) {
  return instance.info?.node?.name || instance.node_name
}

function formatPercent(value?: number) {
  if (typeof value !== 'number' || Number.isNaN(value)) return '-'
  return `${new Intl.NumberFormat(undefined, {
    maximumFractionDigits: 1,
  }).format(value)}%`
}

function formatBytes(bytes?: number): string {
  if (typeof bytes !== 'number' || Number.isNaN(bytes)) return '-'
  if (bytes === 0) return '0 B'
  if (bytes < 0) return `-${formatBytes(-bytes)}`

  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  const index = Math.min(
    Math.floor(Math.log(bytes) / Math.log(1024)),
    units.length - 1
  )
  const value = bytes / 1024 ** index
  return `${new Intl.NumberFormat(undefined, {
    maximumFractionDigits: index === 0 ? 0 : 1,
  }).format(value)} ${units[index]}`
}

function ringColorClass(percent: number | null) {
  if (percent === null) return 'text-muted-foreground/40'
  if (percent >= 90) return 'text-red-500'
  if (percent >= 70) return 'text-amber-500'
  return 'text-emerald-500'
}

type RingProgressProps = {
  percent: number | null
  size?: number
}

function RingProgress(props: RingProgressProps) {
  const size = props.size ?? 22
  const stroke = 2.5
  const radius = (size - stroke) / 2
  const circumference = 2 * Math.PI * radius
  const offset =
    props.percent === null
      ? circumference
      : circumference - (props.percent / 100) * circumference

  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      className='shrink-0 -rotate-90'
      aria-hidden='true'
    >
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill='none'
        strokeWidth={stroke}
        stroke='currentColor'
        className='text-muted'
      />
      <circle
        cx={size / 2}
        cy={size / 2}
        r={radius}
        fill='none'
        strokeWidth={stroke}
        strokeLinecap='round'
        stroke='currentColor'
        strokeDasharray={circumference}
        strokeDashoffset={offset}
        className={cn(
          'transition-[stroke-dashoffset] duration-500',
          ringColorClass(props.percent)
        )}
      />
    </svg>
  )
}

type ResourceCellProps = {
  value?: number
  tooltip?: ReactNode
}

function ResourceCell(props: ResourceCellProps) {
  const percent =
    typeof props.value === 'number' && !Number.isNaN(props.value)
      ? Math.max(0, Math.min(100, props.value))
      : null
  const content = (
    <div className='flex items-center gap-2'>
      <RingProgress percent={percent} />
      <span className='font-mono text-[11px] tabular-nums'>
        {formatPercent(props.value)}
      </span>
    </div>
  )

  if (!props.tooltip) return content

  return (
    <TooltipProvider delay={100}>
      <Tooltip>
        <TooltipTrigger className='block w-full rounded-sm text-left focus-visible:ring-2 focus-visible:outline-none'>
          {content}
        </TooltipTrigger>
        <TooltipContent className='max-w-80'>{props.tooltip}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  )
}

type SystemInstancesTableProps = {
  instances: SystemInstance[]
  deletingNodeName: string | null
  isDeletingInstance: boolean
  onDeleteStaleInstance: (instance: SystemInstance) => void
}

function SystemInstancesList(props: SystemInstancesTableProps) {
  const { t, i18n } = useTranslation()

  return (
    <div className='overflow-x-auto rounded-md border'>
      <Table className='min-w-[1230px]'>
        <TableHeader>
          <TableRow className='bg-muted/40 hover:bg-muted/40'>
            <TableHead className='h-9 min-w-[240px] px-4 text-xs'>
              {t('Instances')}
            </TableHead>
            <TableHead className='h-9 w-[110px] text-xs'>
              {t('Status')}
            </TableHead>
            <TableHead className='h-9 w-[100px] text-xs'>{t('Role')}</TableHead>
            <TableHead className='h-9 w-[96px] text-xs'>{t('CPU')}</TableHead>
            <TableHead className='h-9 w-[96px] text-xs'>
              {t('Memory')}
            </TableHead>
            <TableHead className='h-9 w-[96px] text-xs'>
              {t('Storage')}
            </TableHead>
            <TableHead className='h-9 w-[100px] text-xs'>
              {t('Version')}
            </TableHead>
            <TableHead className='h-9 w-[140px] text-xs'>
              {t('Runtime')}
            </TableHead>
            <TableHead className='h-9 w-[170px] text-xs'>
              {t('Started')}
            </TableHead>
            <TableHead className='h-9 w-[170px] text-xs'>
              {t('Last Seen')}
            </TableHead>
            <TableHead className='h-9 w-[90px] pr-4 text-right text-xs'>
              {t('Actions')}
            </TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {props.instances.map((instance) => {
            const shouldConfigure =
              instance.info?.node?.should_configure_manually === true
            const resources = instance.info?.resources
            const storage = resources?.storage
            const isDeletingThisInstance =
              props.isDeletingInstance &&
              props.deletingNodeName === instance.node_name
            return (
              <TableRow key={instance.node_name} className='hover:bg-muted/30'>
                <TableCell className='px-4 py-2.5 align-middle'>
                  <div className='flex min-w-0 items-center gap-2'>
                    <span
                      className={cn(
                        'size-2 shrink-0 rounded-full',
                        STATUS_DOT_CLASS_NAME[instance.status]
                      )}
                      aria-hidden='true'
                    />
                    <div className='min-w-0'>
                      <div className='flex min-w-0 items-center gap-1.5'>
                        <span className='truncate text-sm font-medium'>
                          {getNodeName(instance)}
                        </span>
                        {shouldConfigure && (
                          <Popover>
                            <PopoverTrigger
                              className='inline-flex shrink-0 rounded-full focus-visible:ring-2 focus-visible:outline-none'
                              aria-label={t('Configure NODE_NAME')}
                            >
                              <Badge
                                variant='outline'
                                className='border-amber-200 bg-amber-50 text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/15 dark:text-amber-300'
                              >
                                <AlertTriangle
                                  className='size-3'
                                  aria-hidden='true'
                                />
                              </Badge>
                            </PopoverTrigger>
                            <PopoverContent align='start' className='w-80'>
                              <PopoverHeader>
                                <PopoverTitle>
                                  {t('Configure NODE_NAME')}
                                </PopoverTitle>
                                <PopoverDescription>
                                  {t(
                                    'This instance is using an automatic hostname. Set NODE_NAME to a stable unique value for multi-instance management.'
                                  )}
                                </PopoverDescription>
                              </PopoverHeader>
                              <div className='space-y-2 text-xs'>
                                <div>
                                  <div className='mb-1 font-medium'>
                                    {t('Example')}
                                  </div>
                                  <code className='bg-muted block rounded-md px-2 py-1.5 font-mono text-[11px] break-all'>
                                    NODE_NAME=new-api-master-1
                                  </code>
                                </div>
                                <p className='text-muted-foreground'>
                                  {t(
                                    'Use a different stable value for each instance, then restart the service.'
                                  )}
                                </p>
                              </div>
                            </PopoverContent>
                          </Popover>
                        )}
                      </div>
                      <div className='text-muted-foreground truncate font-mono text-[11px]'>
                        {instance.info?.host?.hostname || '-'}
                      </div>
                    </div>
                  </div>
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <Badge
                    variant='secondary'
                    className={cn(
                      'gap-1.5',
                      STATUS_CLASS_NAME[instance.status]
                    )}
                  >
                    <span
                      className={cn(
                        'size-1.5 rounded-full',
                        STATUS_DOT_CLASS_NAME[instance.status]
                      )}
                      aria-hidden='true'
                    />
                    {t(instance.status)}
                  </Badge>
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <TooltipProvider delay={100}>
                    <Tooltip>
                      <TooltipTrigger
                        className='inline-flex shrink-0 rounded-full focus-visible:ring-2 focus-visible:outline-none'
                        aria-label={t('Node role')}
                      >
                        <Badge variant='outline'>{roleLabel(instance)}</Badge>
                      </TooltipTrigger>
                      <TooltipContent>
                        {t(roleDescriptionKey(instance))}
                      </TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <ResourceCell value={resources?.cpu?.usage_percent} />
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <ResourceCell value={resources?.memory?.usage_percent} />
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <ResourceCell
                    value={storage?.used_percent}
                    tooltip={
                      storage ? (
                        <div className='space-y-1 text-xs'>
                          <div className='grid grid-cols-[auto_1fr] gap-x-3 gap-y-1'>
                            <span className='text-muted-foreground'>
                              {t('Used')}
                            </span>
                            <span className='font-mono'>
                              {formatBytes(storage.used_bytes)}
                            </span>
                            <span className='text-muted-foreground'>
                              {t('Free')}
                            </span>
                            <span className='font-mono'>
                              {formatBytes(storage.free_bytes)}
                            </span>
                            <span className='text-muted-foreground'>
                              {t('Total')}
                            </span>
                            <span className='font-mono'>
                              {formatBytes(storage.total_bytes)}
                            </span>
                          </div>
                        </div>
                      ) : undefined
                    }
                  />
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <div className='truncate font-mono text-xs'>
                    {instance.info?.runtime?.version || '-'}
                  </div>
                </TableCell>
                <TableCell className='py-2.5 align-middle'>
                  <div className='truncate font-mono text-xs'>
                    {runtimeLabel(instance)}
                  </div>
                </TableCell>
                <TableCell className='text-muted-foreground py-2.5 align-middle text-xs whitespace-nowrap'>
                  {formatTimestampToDate(instance.started_at)}
                </TableCell>
                <TableCell
                  className='text-muted-foreground py-2.5 align-middle text-xs whitespace-nowrap'
                  title={formatTimestampToDate(instance.last_seen_at)}
                >
                  {formatTimestampRelative(
                    instance.last_seen_at,
                    'seconds',
                    toIntlLocale(i18n.language)
                  )}
                </TableCell>
                <TableCell className='py-2.5 pr-4 text-right align-middle'>
                  {instance.status === 'stale' ? (
                    <TooltipProvider delay={100}>
                      <Tooltip>
                        <TooltipTrigger
                          render={
                            <Button
                              type='button'
                              variant='destructive'
                              size='icon-xs'
                              onClick={() =>
                                props.onDeleteStaleInstance(instance)
                              }
                              disabled={
                                props.isDeletingInstance ||
                                isDeletingThisInstance
                              }
                              aria-label={t('Delete stale instance')}
                            >
                              {isDeletingThisInstance ? (
                                <Loader2
                                  className='size-3 animate-spin'
                                  aria-hidden='true'
                                />
                              ) : (
                                <Trash2 className='size-3' aria-hidden='true' />
                              )}
                            </Button>
                          }
                        />
                        <TooltipContent>
                          {t('Delete stale instance')}
                        </TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  ) : (
                    <span className='text-muted-foreground text-xs'>-</span>
                  )}
                </TableCell>
              </TableRow>
            )
          })}
        </TableBody>
      </Table>
    </div>
  )
}

export function SystemInstancesPanel() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [deleteTarget, setDeleteTarget] = useState<SystemInstance | null>(null)
  const [deleteAllConfirmOpen, setDeleteAllConfirmOpen] = useState(false)
  const [deletingNodeName, setDeletingNodeName] = useState<string | null>(null)
  const instancesQuery = useQuery({
    queryKey: ['system-info', 'instances'],
    queryFn: async () => {
      const res = await listSystemInstances()
      if (!res.success || !Array.isArray(res.data)) {
        throw createServerError(res, t('We could not load instances.'))
      }
      return res.data
    },
    staleTime: 30 * 1000,
    retry: false,
    refetchInterval: INSTANCE_POLL_INTERVAL_MS,
  })

  const instances = instancesQuery.data ?? []
  const staleInstances = instances.filter(
    (instance) => instance.status === 'stale'
  )
  const hasStaleInstances = staleInstances.length > 0
  const loading = instancesQuery.isLoading
  const refreshing = instancesQuery.isFetching && !instancesQuery.isLoading

  const invalidateInstances = async () => {
    await queryClient.invalidateQueries({
      queryKey: ['system-info', 'instances'],
    })
  }

  const deleteStaleInstanceMutation = useMutation({
    mutationFn: async (nodeName: string) => {
      const res = await deleteStaleSystemInstance(nodeName)
      if (!res.success) {
        throw createServerError(res, t('Delete failed'))
      }
      return res
    },
    onMutate: (nodeName) => {
      setDeletingNodeName(nodeName)
    },
    onSuccess: async () => {
      toast.success(t('Deleted stale instance'))
      await invalidateInstances()
      setDeleteTarget(null)
    },
    onError: (error) => {
      handleServerError(error, t('Delete failed'))
      void invalidateInstances()
    },
    onSettled: () => {
      setDeletingNodeName(null)
    },
  })

  const deleteStaleInstancesMutation = useMutation({
    mutationFn: async () => {
      const res = await deleteStaleSystemInstances()
      if (!res.success) {
        throw createServerError(res, t('Delete failed'))
      }
      return res
    },
    onSuccess: async (res) => {
      toast.success(
        t('Deleted {{count}} stale instances', {
          count: res.data?.deleted_count ?? 0,
        })
      )
      await invalidateInstances()
      setDeleteAllConfirmOpen(false)
    },
    onError: (error) => {
      handleServerError(error, t('Delete failed'))
    },
  })

  const isMutatingInstance =
    deletingNodeName !== null || deleteStaleInstanceMutation.isPending

  let instancesContent: ReactNode
  if (loading) {
    instancesContent = (
      <div className='space-y-2 p-4 sm:p-5'>
        {INSTANCE_SKELETON_KEYS.map((key) => (
          <Skeleton key={key} className='h-9 w-full rounded-md' />
        ))}
      </div>
    )
  } else if (instancesQuery.isError) {
    instancesContent = (
      <ErrorState
        title={t('We could not load instances.')}
        description={
          instancesQuery.error instanceof Error
            ? instancesQuery.error.message
            : undefined
        }
        onRetry={() => {
          void instancesQuery.refetch()
        }}
        className='min-h-[220px]'
      />
    )
  } else if (instances.length === 0) {
    instancesContent = (
      <div className='px-4 py-10 text-center sm:px-5'>
        <div className='bg-muted mx-auto mb-3 flex size-10 items-center justify-center rounded-lg'>
          <ServerCog
            className='text-muted-foreground size-5'
            aria-hidden='true'
          />
        </div>
        <p className='text-muted-foreground text-sm'>
          {t('No instances have reported yet.')}
        </p>
      </div>
    )
  } else {
    instancesContent = (
      <div className='p-4 sm:p-5'>
        <SystemInstancesList
          instances={instances}
          deletingNodeName={deletingNodeName}
          isDeletingInstance={
            isMutatingInstance || deleteStaleInstancesMutation.isPending
          }
          onDeleteStaleInstance={setDeleteTarget}
        />
      </div>
    )
  }

  return (
    <>
      <section className='bg-card overflow-hidden rounded-lg border shadow-xs'>
        <div className='flex flex-col gap-3 border-b px-4 py-3 sm:flex-row sm:items-center sm:justify-between sm:px-5'>
          <div className='min-w-0'>
            <div className='flex items-center gap-2'>
              <span className='bg-muted text-muted-foreground inline-flex size-7 items-center justify-center rounded-md'>
                <ServerCog className='size-4' aria-hidden='true' />
              </span>
              <div className='min-w-0'>
                <h3 className='text-sm font-semibold'>{t('Instances')}</h3>
                <p className='text-muted-foreground mt-0.5 text-xs'>
                  {t(
                    'Nodes reporting from this deployment and their latest heartbeat.'
                  )}
                </p>
              </div>
            </div>
          </div>
          <div className='flex shrink-0 flex-wrap items-center gap-2 sm:justify-end'>
            <span className='text-muted-foreground text-xs' aria-live='polite'>
              {t('Auto-refreshing every {{seconds}}s', {
                seconds: INSTANCE_POLL_INTERVAL_MS / 1000,
              })}
            </span>
            {hasStaleInstances ? (
              <Button
                type='button'
                variant='destructive'
                size='sm'
                onClick={() => setDeleteAllConfirmOpen(true)}
                disabled={
                  isMutatingInstance || deleteStaleInstancesMutation.isPending
                }
              >
                {deleteStaleInstancesMutation.isPending ? (
                  <Loader2
                    data-icon='inline-start'
                    className='size-3.5 animate-spin'
                    aria-hidden='true'
                  />
                ) : (
                  <Trash2
                    data-icon='inline-start'
                    className='size-3.5'
                    aria-hidden='true'
                  />
                )}
                {t('Delete all stale')}
              </Button>
            ) : null}
            <Button
              type='button'
              variant='outline'
              size='sm'
              onClick={() => void instancesQuery.refetch()}
              disabled={instancesQuery.isFetching}
              aria-label={t('Refresh')}
            >
              <RefreshCw
                data-icon='inline-start'
                className={cn('size-3.5', refreshing && 'animate-spin')}
                aria-hidden='true'
              />
              {refreshing ? t('Refreshing...') : t('Refresh')}
            </Button>
          </div>
        </div>

        <div aria-busy={instancesQuery.isFetching}>{instancesContent}</div>
      </section>

      <ConfirmDialog
        open={deleteAllConfirmOpen}
        onOpenChange={setDeleteAllConfirmOpen}
        title={t('Delete stale instances')}
        desc={t(
          'Delete {{count}} stale instance records? Online instances will not be deleted.',
          { count: staleInstances.length }
        )}
        destructive
        isLoading={deleteStaleInstancesMutation.isPending}
        confirmText={
          deleteStaleInstancesMutation.isPending
            ? t('Deleting...')
            : t('Delete')
        }
        handleConfirm={() => deleteStaleInstancesMutation.mutate()}
      />

      <ConfirmDialog
        open={deleteTarget !== null}
        onOpenChange={(open) => {
          if (!open) setDeleteTarget(null)
        }}
        title={t('Delete stale instance')}
        desc={
          deleteTarget
            ? t(
                'Delete stale instance "{{name}}"? If it has reported again, it will not be deleted.',
                { name: getNodeName(deleteTarget) }
              )
            : ''
        }
        destructive
        isLoading={deleteStaleInstanceMutation.isPending}
        confirmText={
          deleteStaleInstanceMutation.isPending ? t('Deleting...') : t('Delete')
        }
        handleConfirm={() => {
          if (!deleteTarget) return
          deleteStaleInstanceMutation.mutate(deleteTarget.node_name)
        }}
      />
    </>
  )
}
