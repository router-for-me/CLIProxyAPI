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
import { Plus } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { BadgeCell } from '@/components/data-table/core/badge-cell'
import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'

import { useDeleteProvider } from '../hooks/use-custom-oauth-mutations'
import type { CustomOAuthProvider } from '../types'

type ProviderTableProps = {
  providers: CustomOAuthProvider[]
  onEdit: (provider: CustomOAuthProvider) => void
  onCreate: () => void
}

export function ProviderTable(props: ProviderTableProps) {
  const { t } = useTranslation()
  const deleteProvider = useDeleteProvider()
  const [deleteTarget, setDeleteTarget] = useState<CustomOAuthProvider | null>(
    null
  )

  const handleDelete = async () => {
    if (!deleteTarget) return
    await deleteProvider.mutateAsync(deleteTarget.id)
    setDeleteTarget(null)
  }

  return (
    <>
      <div className='flex items-center justify-between'>
        <p className='text-muted-foreground text-sm'>
          {t('Manage custom OAuth providers for user authentication')}
        </p>
        <Button size='sm' onClick={props.onCreate}>
          <Plus className='mr-1.5 h-4 w-4' />
          {t('Add Provider')}
        </Button>
      </div>

      <StaticDataTable
        data={props.providers}
        getRowKey={(provider) => provider.id}
        emptyClassName='text-sm'
        emptyContent={t('No custom OAuth providers configured yet.')}
        columns={[
          {
            id: 'icon',
            header: t('Icon'),
            cell: (provider) =>
              provider.icon ? (
                <span className='text-lg'>{provider.icon}</span>
              ) : (
                <span className='text-muted-foreground text-sm'>--</span>
              ),
          },
          {
            id: 'name',
            header: t('Name'),
            cellClassName: 'font-medium',
            cell: (provider) => provider.name,
          },
          {
            id: 'slug',
            header: t('Slug'),
            cell: (provider) => (
              <BadgeCell>
                <StatusBadge
                  label={provider.slug}
                  variant='neutral'
                  copyable={false}
                />
              </BadgeCell>
            ),
          },
          {
            id: 'status',
            header: t('Status'),
            cell: (provider) => (
              <BadgeCell>
                <StatusBadge
                  label={provider.enabled ? t('Enabled') : t('Disabled')}
                  variant={provider.enabled ? 'success' : 'neutral'}
                  copyable={false}
                />
              </BadgeCell>
            ),
          },
          {
            id: 'client-id',
            header: t('Client ID'),
            cellClassName:
              'text-muted-foreground max-w-[120px] truncate font-mono',
            cell: (provider) => provider.client_id,
          },
          {
            id: 'actions',
            header: t('Actions'),
            className: 'text-right',
            cellClassName: 'text-right',
            cell: (provider) => (
              <StaticRowActions
                editLabel={t('Edit')}
                deleteLabel={t('Delete')}
                menuLabel={t('Open menu')}
                onEdit={() => props.onEdit(provider)}
                onDelete={() => setDeleteTarget(provider)}
              />
            ),
          },
        ]}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title={t('Delete Provider')}
        desc={t(
          'Are you sure you want to delete "{{name}}"? Users who authenticated with this provider will no longer be able to log in.',
          { name: deleteTarget?.name || '' }
        )}
        confirmText={t('Delete')}
        destructive
        handleConfirm={handleDelete}
        isLoading={deleteProvider.isPending}
      />
    </>
  )
}
