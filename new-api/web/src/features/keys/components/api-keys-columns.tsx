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
import type { ColumnDef } from '@tanstack/react-table'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StatusBadge } from '@/components/status-badge'
import { Checkbox } from '@/components/ui/checkbox'
import { useMediaQuery } from '@/hooks'
import { toIntlLocale } from '@/i18n/languages'
import { getUserGroups } from '@/lib/api'
import { getCurrencyDisplay } from '@/lib/currency'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { API_KEY_STATUSES } from '../constants'
import type { ApiKey } from '../types'
import { ApiKeyGroupCell } from './api-key-group-cell'
import { ApiKeyQuotaCell } from './api-key-quota-cell'
import {
  ApiKeyActivityCell,
  ApiKeyTimestampCell,
} from './api-key-timestamp-cell'
import {
  ApiKeyCell,
  IpRestrictionsCell,
  ModelLimitsCell,
} from './api-keys-cells'
import { DataTableRowActions } from './data-table-row-actions'

const EMPTY_GROUP_RATIOS: Record<string, number | string> = {}

function useGroupRatios(): Record<string, number | string> {
  const { data } = useQuery({
    queryKey: ['user-groups'],
    queryFn: async () => requireServerSuccess(await getUserGroups()),
    staleTime: 0,
    select: (res) => {
      if (!res.success || !res.data) return {}
      const ratios: Record<string, number | string> = {}
      for (const [group, info] of Object.entries(res.data)) {
        if (typeof info.ratio === 'number' || typeof info.ratio === 'string') {
          ratios[group] = info.ratio
        }
      }
      return ratios
    },
  })

  return data ?? EMPTY_GROUP_RATIOS
}

export function useApiKeysColumns(now: number): ColumnDef<ApiKey>[] {
  const { t, i18n } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)
  const { meta: currency } = getCurrencyDisplay()
  const quotaUnit = currency.kind === 'tokens' ? t('Tokens') : currency.symbol
  const groupRatios = useGroupRatios()
  const shouldReduceMotion = useMediaQuery('(prefers-reduced-motion: reduce)')
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const justNowLabel = t('Just now')
  return useMemo<ColumnDef<ApiKey>[]>(
    () => [
      {
        id: 'select',
        header: ({ table }) => (
          <Checkbox
            checked={table.getIsAllPageRowsSelected()}
            indeterminate={table.getIsSomePageRowsSelected()}
            onCheckedChange={(value) =>
              table.toggleAllPageRowsSelected(!!value)
            }
            aria-label={t('Select all')}
            className='translate-y-[2px]'
          />
        ),
        cell: ({ row }) => (
          <Checkbox
            checked={row.getIsSelected()}
            onCheckedChange={(value) => row.toggleSelected(!!value)}
            aria-label={t('Select row')}
            className='translate-y-[2px]'
          />
        ),
        enableSorting: false,
        enableHiding: false,
        size: 40,
      },
      {
        accessorKey: 'name',
        header: t('Name'),
        cell: ({ row }) => (
          <span className='font-medium'>{row.getValue('name')}</span>
        ),
        size: 180,
        meta: { mobileTitle: true },
      },
      {
        accessorKey: 'status',
        header: t('Status'),
        cell: ({ row }) => {
          const statusConfig =
            API_KEY_STATUSES[row.getValue('status') as number]
          if (!statusConfig) return null
          return (
            <StatusBadge
              label={t(statusConfig.label)}
              variant={statusConfig.variant}
              copyable={false}
              className='-ml-1.5'
            />
          )
        },
        filterFn: (row, id, value) => value.includes(String(row.getValue(id))),
        size: 120,
        meta: { mobileBadge: true },
      },
      {
        id: 'key',
        accessorKey: 'key',
        header: t('API Key'),
        cell: ({ row }) => <ApiKeyCell apiKey={row.original} />,
        enableSorting: false,
        size: 260,
      },
      {
        id: 'quota',
        accessorKey: 'remain_quota',
        header: `${t('Quota')} (${quotaUnit})`,
        cell: ({ row }) => <ApiKeyQuotaCell apiKey={row.original} now={now} />,
        size: 260,
        minSize: 260,
      },
      {
        accessorKey: 'group',
        header: t('Group'),
        cell: ({ row }) => {
          const apiKey = row.original
          const group = row.getValue('group') as string
          return (
            <ApiKeyGroupCell
              group={group}
              ratio={groupRatios[group]}
              crossGroupRetry={apiKey.cross_group_retry}
              shouldReduceMotion={shouldReduceMotion}
            />
          )
        },
        size: 220,
        meta: { mobileHidden: true },
      },
      {
        id: 'model_limits',
        accessorKey: 'model_limits',
        header: t('Models'),
        cell: ({ row }) => <ModelLimitsCell apiKey={row.original} />,
        enableSorting: false,
        size: 160,
        meta: { mobileHidden: true },
      },
      {
        id: 'allow_ips',
        accessorKey: 'allow_ips',
        header: t('IP Restriction'),
        cell: ({ row }) => <IpRestrictionsCell apiKey={row.original} />,
        enableSorting: false,
        size: 160,
        meta: { mobileHidden: true },
      },
      {
        id: 'activity_time',
        accessorKey: 'created_time',
        header: t('Time'),
        cell: ({ row }) => (
          <ApiKeyActivityCell apiKey={row.original} now={now} />
        ),
        size: 220,
        meta: { mobileHidden: true },
      },
      {
        accessorKey: 'expired_time',
        header: t('Expires'),
        cell: ({ row }) => {
          const expiredTime = row.getValue('expired_time') as number
          if (expiredTime === -1) {
            return (
              <StatusBadge
                label={t('Never')}
                variant='neutral'
                copyable={false}
                className='-ml-1.5'
              />
            )
          }
          return (
            <ApiKeyTimestampCell
              timestamp={expiredTime}
              now={now}
              locale={locale}
              justNowLabel={justNowLabel}
              className={
                expiredTime * 1000 <= now
                  ? 'text-destructive'
                  : 'text-muted-foreground'
              }
            />
          )
        },
        size: 180,
        meta: { mobileHidden: true },
      },
      {
        id: 'actions',
        header: () => t('Actions'),
        cell: ({ row }) => <DataTableRowActions row={row} />,
        meta: { pinned: 'right' as const },
      },
    ],
    [t, quotaUnit, now, groupRatios, shouldReduceMotion, locale, justNowLabel]
  )
}
