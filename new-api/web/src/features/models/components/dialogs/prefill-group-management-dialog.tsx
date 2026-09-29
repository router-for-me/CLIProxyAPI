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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  Layers3,
  Loader2,
  Pencil,
  Plus,
  RefreshCcw,
  Trash2,
} from 'lucide-react'
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Dialog } from '@/components/dialog'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { useIsMobile } from '@/hooks/use-mobile'
import { handleServerError } from '@/lib/handle-server-error'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import { deletePrefillGroup, getPrefillGroups } from '../../api'
import { prefillGroupsQueryKeys } from '../../lib'
import type { PrefillGroup } from '../../types'
import {
  PREFILL_GROUP_TYPE_META,
  parseEndpointKeys,
  parseStringItems,
} from '../prefill-group-shared'

type PrefillGroupManagementDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreateGroup: () => void
  onEditGroup: (group: PrefillGroup) => void
}

export function PrefillGroupManagementDialog({
  open,
  onOpenChange,
  onCreateGroup,
  onEditGroup,
}: PrefillGroupManagementDialogProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const isMobile = useIsMobile()
  const [deleteState, setDeleteState] = useState<{
    open: boolean
    group: PrefillGroup | null
  }>({ open: false, group: null })
  const [isDeleting, setIsDeleting] = useState(false)

  const {
    data,
    isLoading,
    isFetching,
    error,
    refetch: refetchGroups,
  } = useQuery({
    queryKey: prefillGroupsQueryKeys.list(),
    queryFn: async () => requireServerSuccess(await getPrefillGroups()),
    enabled: open,
  })

  const groups = useMemo(() => data?.data ?? [], [data?.data])

  const sortedGroups = useMemo(
    () =>
      [...groups].sort((a, b) => {
        if (a.type === b.type) {
          return a.name.localeCompare(b.name)
        }
        return a.type.localeCompare(b.type)
      }),
    [groups]
  )

  const normalizedGroups = useMemo(
    () =>
      sortedGroups.map((group) => {
        const meta = PREFILL_GROUP_TYPE_META[group.type] || {
          label: group.type,
          badge: 'neutral' as const,
        }
        const parsedItems =
          group.type === 'endpoint'
            ? parseEndpointKeys(group.items)
            : parseStringItems(group.items)
        return { group, meta, parsedItems }
      }),
    [sortedGroups]
  )

  useEffect(() => {
    if (!open) {
      setDeleteState({ open: false, group: null })
      setIsDeleting(false)
    }
  }, [open])

  const handleDeleteClick = (group: PrefillGroup) => {
    setDeleteState({ open: true, group })
  }

  const handleDeleteConfirm = async () => {
    if (!deleteState.group) return
    setIsDeleting(true)
    try {
      const response = await deletePrefillGroup(deleteState.group.id)
      if (response.success) {
        toast.success(t('Deleted "{{name}}"', { name: deleteState.group.name }))
        queryClient.invalidateQueries({
          queryKey: prefillGroupsQueryKeys.lists(),
        })
        setDeleteState({ open: false, group: null })
      } else {
        handleServerError(response, t('Failed to delete group'))
      }
    } catch (err: unknown) {
      handleServerError(err, t('Failed to delete group'))
    } finally {
      setIsDeleting(false)
    }
  }

  let groupsContent: ReactNode
  if (isLoading) {
    groupsContent = (
      <div className='flex flex-col items-center justify-center gap-2 py-12 text-center'>
        <Loader2 className='text-muted-foreground h-6 w-6 animate-spin' />
        <p className='text-muted-foreground text-sm'>
          {t('Fetching prefill groups...')}
        </p>
      </div>
    )
  } else if (normalizedGroups.length === 0) {
    groupsContent = (
      <Empty className='border border-dashed py-10'>
        <EmptyMedia variant='icon'>
          <Layers3 className='h-6 w-6' />
        </EmptyMedia>
        <EmptyHeader>
          <EmptyTitle>{t('No prefill groups yet')}</EmptyTitle>
          <EmptyDescription>
            {t(
              'Create your first group to reuse model, tag, or endpoint selections anywhere in the dashboard.'
            )}
          </EmptyDescription>
        </EmptyHeader>
        <EmptyDescription>
          {t('Prefill groups help you keep complex configurations in sync.')}
        </EmptyDescription>
      </Empty>
    )
  } else if (isMobile) {
    groupsContent = (
      <div className='space-y-3'>
        {normalizedGroups.map(({ group, meta, parsedItems }) => (
          <Card key={group.id} className='border-border/60'>
            <CardHeader className='flex flex-row items-start justify-between gap-4'>
              <div className='space-y-2'>
                <CardTitle className='flex flex-wrap items-center gap-2'>
                  {group.name}
                  <StatusBadge variant={meta.badge} size='sm' copyable={false}>
                    {meta.label}
                    <span className='text-muted-foreground/30'>·</span>
                    <span className='text-muted-foreground font-mono'>
                      #{group.id}
                    </span>
                  </StatusBadge>
                </CardTitle>
                {group.description ? (
                  <CardDescription className='line-clamp-2'>
                    {group.description}
                  </CardDescription>
                ) : (
                  <CardDescription className='text-muted-foreground italic'>
                    No description provided
                  </CardDescription>
                )}
              </div>

              <div className='flex items-center gap-2'>
                <Button
                  size='icon'
                  variant='outline'
                  onClick={() => onEditGroup(group)}
                >
                  <Pencil className='h-4 w-4' />
                  <span className='sr-only'>{t('Edit group')}</span>
                </Button>
                <Button
                  size='icon'
                  variant='ghost'
                  className='text-destructive hover:text-destructive'
                  onClick={() => handleDeleteClick(group)}
                >
                  <Trash2 className='h-4 w-4' />
                  <span className='sr-only'>{t('Delete group')}</span>
                </Button>
              </div>
            </CardHeader>
            <CardContent className='space-y-3'>
              <div className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs font-medium tracking-wide uppercase'>
                <span>Items</span>
                <StatusBadge
                  label={`${parsedItems.length} item${parsedItems.length === 1 ? '' : 's'}`}
                  variant='neutral'
                  size='sm'
                  copyable={false}
                />
              </div>
              {parsedItems.length > 0 ? (
                <div className='flex flex-wrap gap-2'>
                  {parsedItems.slice(0, 6).map((item) => (
                    <StatusBadge
                      key={item}
                      label={item}
                      autoColor={item}
                      size='sm'
                    />
                  ))}
                  {parsedItems.length > 6 && (
                    <StatusBadge
                      label={`+${parsedItems.length - 6} more`}
                      variant='neutral'
                      size='sm'
                      copyable={false}
                    />
                  )}
                </div>
              ) : (
                <p className='text-muted-foreground text-sm'>
                  {group.type === 'endpoint'
                    ? 'No endpoint mappings configured.'
                    : 'No items configured yet.'}
                </p>
              )}
            </CardContent>
          </Card>
        ))}
      </div>
    )
  } else {
    groupsContent = (
      <StaticDataTable
        tableClassName='min-w-[680px]'
        data={normalizedGroups}
        getRowKey={({ group }) => group.id}
        columns={[
          {
            id: 'group',
            header: t('Group'),
            cellClassName: 'align-top whitespace-normal',
            cell: ({ group }) => (
              <div className='flex flex-col gap-1'>
                <div className='flex flex-wrap items-center gap-2'>
                  <span className='font-medium'>{group.name}</span>
                  <TableId value={group.id} />
                </div>
                {group.description ? (
                  <p className='text-muted-foreground text-xs'>
                    {group.description}
                  </p>
                ) : (
                  <p className='text-muted-foreground text-xs italic'>
                    No description provided
                  </p>
                )}
              </div>
            ),
          },
          {
            id: 'type',
            header: t('Type'),
            cellClassName: 'align-top',
            cell: ({ meta }) => (
              <StatusBadge
                label={meta.label}
                variant={meta.badge}
                size='sm'
                copyable={false}
              />
            ),
          },
          {
            id: 'items',
            header: t('Items'),
            className: 'min-w-[240px]',
            cellClassName: 'align-top whitespace-normal',
            cell: ({ group, parsedItems }) => (
              <>
                <div className='flex flex-wrap gap-2'>
                  {parsedItems.length > 0 ? (
                    <>
                      {parsedItems.slice(0, 6).map((item) => (
                        <StatusBadge
                          key={item}
                          label={item}
                          autoColor={item}
                          size='sm'
                        />
                      ))}
                      {parsedItems.length > 6 && (
                        <StatusBadge
                          label={`+${parsedItems.length - 6} more`}
                          variant='neutral'
                          size='sm'
                          copyable={false}
                        />
                      )}
                    </>
                  ) : (
                    <p className='text-muted-foreground text-sm'>
                      {group.type === 'endpoint'
                        ? 'No endpoint mappings configured.'
                        : 'No items configured yet.'}
                    </p>
                  )}
                </div>
                <div className='text-muted-foreground mt-2 text-xs font-medium tracking-wide uppercase'>
                  {parsedItems.length} item
                  {parsedItems.length === 1 ? '' : 's'}
                </div>
              </>
            ),
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'align-top',
            cell: ({ group }) => (
              <StaticRowActions
                editLabel={t('Edit group')}
                deleteLabel={t('Delete group')}
                menuLabel={t('Open menu')}
                onEdit={() => onEditGroup(group)}
                onDelete={() => handleDeleteClick(group)}
              />
            ),
          },
        ]}
      />
    )
  }

  return (
    <>
      <Dialog
        open={open}
        onOpenChange={onOpenChange}
        title={
          <>
            <Layers3 className='text-foreground/80 h-5 w-5' />
            {t('Prefill Group Management')}
          </>
        }
        description={t(
          'Create reusable bundles of models, tags, endpoints, and user groups to speed up configuration elsewhere in the console.'
        )}
        contentClassName={cn(
          'w-[calc(100vw-2rem)] sm:max-w-[52rem]',
          isMobile && 'max-w-none rounded-none'
        )}
        titleClassName='flex flex-wrap items-center gap-2 text-lg'
        descriptionClassName='text-sm leading-relaxed'
        contentHeight='auto'
        bodyClassName={cn(
          'space-y-3',
          isMobile && 'pb-[calc(env(safe-area-inset-bottom,0px)+1rem)]'
        )}
      >
        <div className='bg-muted/30 flex flex-wrap items-center justify-between gap-3 rounded-md border p-2 text-sm'>
          <div className='flex flex-wrap items-center gap-2'>
            <Button size='sm' onClick={onCreateGroup}>
              <Plus className='mr-2 h-4 w-4' />
              {t('New Group')}
            </Button>
            <Button
              size='sm'
              variant='ghost'
              onClick={() => refetchGroups()}
              disabled={isFetching}
            >
              {isFetching ? (
                <Loader2 className='mr-2 h-4 w-4 animate-spin' />
              ) : (
                <RefreshCcw className='mr-2 h-4 w-4' />
              )}
              {t('Refresh')}
            </Button>
          </div>
          <StatusBadge
            label={`${groups.length} group${groups.length === 1 ? '' : 's'}`}
            variant='neutral'
            copyable={false}
          />
        </div>

        <div className='flex flex-col gap-3'>
          {error && (
            <Alert variant='destructive'>
              <AlertTitle>{t('Unable to load groups')}</AlertTitle>
              <AlertDescription>
                {(error as Error).message ||
                  'Please retry or refresh the page.'}
              </AlertDescription>
            </Alert>
          )}

          {groupsContent}
        </div>
      </Dialog>

      <ConfirmDialog
        open={deleteState.open}
        onOpenChange={(next) => setDeleteState({ open: next, group: null })}
        title={t('Delete group')}
        desc={
          <p>
            {t(
              'Are you sure you want to delete group "{{name}}"? This action cannot be undone.',
              { name: deleteState.group?.name ?? '' }
            )}
          </p>
        }
        destructive
        confirmText={isDeleting ? t('Deleting...') : t('Delete')}
        isLoading={isDeleting}
        handleConfirm={handleDeleteConfirm}
      />
    </>
  )
}
