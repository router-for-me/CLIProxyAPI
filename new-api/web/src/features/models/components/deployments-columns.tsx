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
import type { ColumnDef } from '@tanstack/react-table'
import { Eye, Info, Pencil, Settings2, Timer, Trash2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { DataTableRowActionMenu } from '@/components/data-table/core/row-action-menu'
import { StatusBadge } from '@/components/status-badge'
import { TableId } from '@/components/table-id'
import { Button } from '@/components/ui/button'
import {
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuShortcut,
} from '@/components/ui/dropdown-menu'
import { formatTimestampToDate } from '@/lib/format'

import { getDeploymentStatusConfig } from '../constants'
import {
  formatRemainingMinutes,
  normalizeDeploymentStatus,
} from '../lib/deployments-utils'
import type { Deployment } from '../types'

export function useDeploymentsColumns(opts: {
  onViewLogs: (id: string | number) => void
  onViewDetails: (id: string | number) => void
  onUpdateConfig: (id: string | number) => void
  onExtend: (id: string | number) => void
  onRename: (id: string | number, currentName: string) => void
  onDelete: (deployment: Deployment) => void
}): ColumnDef<Deployment>[] {
  const { t } = useTranslation()
  const STATUS = getDeploymentStatusConfig(t)

  return [
    {
      accessorKey: 'id',
      header: t('ID'),
      meta: { mobileHidden: true },
      cell: ({ row }) => {
        const id = row.original.id
        return <TableId value={id} />
      },
      size: 120,
    },
    {
      id: 'name',
      accessorFn: (row) =>
        row.container_name || row.deployment_name || row.name || '',
      header: t('Name'),
      meta: { mobileTitle: true },
      cell: ({ getValue }) => {
        const name = String(getValue() || '-') || '-'
        return (
          <StatusBadge
            label={name}
            variant='neutral'
            copyText={name}
            size='sm'
            className='-ml-1.5 font-mono'
          />
        )
      },
      minSize: 220,
    },
    {
      accessorKey: 'status',
      header: t('Status'),
      meta: { mobileBadge: true },
      cell: ({ row }) => {
        const raw = row.original.status
        const key = normalizeDeploymentStatus(raw)
        const config = STATUS[key] || {
          label:
            typeof raw === 'string' && raw.trim() ? raw.trim() : t('Unknown'),
          variant: 'neutral' as const,
        }
        return (
          <StatusBadge
            label={config.label}
            variant={config.variant}
            size='sm'
            copyable={false}
            className='-ml-1.5'
          />
        )
      },
      filterFn: (row, id, value) => {
        if (
          !Array.isArray(value) ||
          value.length === 0 ||
          value.includes('all')
        ) {
          return true
        }
        const status = normalizeDeploymentStatus(row.getValue(id))
        return value.includes(status)
      },
      size: 160,
      enableSorting: false,
    },
    {
      accessorKey: 'provider',
      header: t('Provider'),
      cell: ({ row }) => {
        const provider = row.original.provider
        if (!provider) {
          return <span className='text-muted-foreground text-xs'>-</span>
        }
        return (
          <StatusBadge
            label={String(provider)}
            autoColor={String(provider)}
            size='sm'
            copyable={false}
            className='-ml-1.5'
          />
        )
      },
      size: 140,
      enableSorting: false,
    },
    {
      accessorKey: 'time_remaining',
      header: t('Time remaining'),
      cell: ({ row }) => {
        const status = normalizeDeploymentStatus(row.original.status)
        const remainingText =
          typeof row.original.time_remaining === 'string' &&
          row.original.time_remaining.trim()
            ? row.original.time_remaining.trim()
            : '-'
        const remainingHuman = formatRemainingMinutes(
          row.original.compute_minutes_remaining
        )
        const percentUsed =
          typeof row.original.completed_percent === 'number' &&
          Number.isFinite(row.original.completed_percent)
            ? Math.max(
                0,
                Math.min(100, Math.round(row.original.completed_percent))
              )
            : null
        const percentRemain =
          percentUsed === null
            ? null
            : Math.max(0, Math.min(100, 100 - percentUsed))

        return (
          <div className='flex flex-col gap-1 text-sm'>
            <div className='flex flex-wrap items-center gap-2'>
              <span className='font-medium'>{remainingText}</span>
              {status === 'running' && percentRemain !== null ? (
                <StatusBadge
                  label={`${percentRemain}%`}
                  variant='info'
                  size='sm'
                  copyable={false}
                />
              ) : null}
            </div>
            {remainingHuman ? (
              <div className='text-muted-foreground text-xs'>
                {t('Approx.')} {remainingHuman}
              </div>
            ) : null}
          </div>
        )
      },
      minSize: 220,
      enableSorting: false,
    },
    {
      id: 'hardware',
      header: t('Hardware'),
      meta: { mobileHidden: true },
      accessorFn: (row) =>
        row.hardware_info || row.hardware_name || row.brand_name || '',
      cell: ({ row }) => {
        const hardware =
          row.original.hardware_name ||
          (typeof row.original.hardware_info === 'string'
            ? row.original.hardware_info
            : '')
        const qty =
          typeof row.original.hardware_quantity === 'number'
            ? row.original.hardware_quantity
            : null
        if (!hardware) {
          return <span className='text-muted-foreground text-xs'>-</span>
        }
        return (
          <div className='flex max-w-full min-w-0 flex-nowrap items-center gap-2 overflow-hidden'>
            <StatusBadge
              label={String(hardware)}
              variant='neutral'
              copyText={String(hardware)}
              size='sm'
            />
            {qty !== null ? (
              <span className='text-muted-foreground text-xs'>×{qty}</span>
            ) : null}
          </div>
        )
      },
      minSize: 220,
      enableSorting: false,
    },
    {
      accessorKey: 'created_at',
      header: t('Created'),
      meta: { mobileHidden: true },
      cell: ({ row }) => {
        let ts: number | undefined
        if (typeof row.original.created_at === 'number') {
          ts = row.original.created_at
        } else if (typeof row.original.created_at === 'string') {
          ts = Number(row.original.created_at)
        }
        return (
          <div className='min-w-[140px] font-mono text-sm'>
            {formatTimestampToDate(ts)}
          </div>
        )
      },
      size: 180,
    },
    {
      id: 'actions',
      header: () => t('Actions'),
      enableHiding: false,
      enableSorting: false,
      cell: ({ row }) => {
        const id = row.original.id
        const currentName =
          row.original.container_name ||
          row.original.deployment_name ||
          row.original.name ||
          ''

        return (
          <div className='-ml-2.5 flex items-center gap-1'>
            <Button
              variant='ghost'
              size='icon-sm'
              onClick={() => opts.onViewLogs(id)}
              aria-label={t('View logs')}
            >
              <Eye />
            </Button>
            <DataTableRowActionMenu ariaLabel={t('Open menu')}>
              <DropdownMenuItem onClick={() => opts.onViewDetails(id)}>
                {t('View details')}
                <DropdownMenuShortcut>
                  <Info size={16} />
                </DropdownMenuShortcut>
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => opts.onUpdateConfig(id)}>
                {t('Update configuration')}
                <DropdownMenuShortcut>
                  <Settings2 size={16} />
                </DropdownMenuShortcut>
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => opts.onExtend(id)}>
                {t('Extend deployment')}
                <DropdownMenuShortcut>
                  <Timer size={16} />
                </DropdownMenuShortcut>
              </DropdownMenuItem>
              <DropdownMenuItem onClick={() => opts.onRename(id, currentName)}>
                {t('Rename deployment')}
                <DropdownMenuShortcut>
                  <Pencil size={16} />
                </DropdownMenuShortcut>
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem
                onClick={() => opts.onDelete(row.original)}
                className='text-destructive focus:text-destructive'
              >
                {t('Delete')}
                <DropdownMenuShortcut>
                  <Trash2 size={16} />
                </DropdownMenuShortcut>
              </DropdownMenuItem>
            </DataTableRowActionMenu>
          </div>
        )
      },
      meta: { pinned: 'right' as const },
    },
  ]
}
