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
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Combobox } from '@/components/ui/combobox'
import { Label } from '@/components/ui/label'
import { getLobeIcon } from '@/lib/lobe-icon'
import { createServerError } from '@/lib/server-error-message'

import { getVendors } from '../../api'
import { vendorsQueryKeys } from '../../lib'
import {
  applyVendorOperation,
  invalidateVendorData,
  previewVendorOperation,
  vendorErrorMessage,
  type VendorOperation,
} from '../../vendor-api'

// Mount a fresh dialog for each explicit selection. Previewing never writes.
export function VendorOperationDialog(props: {
  selection: VendorOperation
  onClose: () => void
  onSuccess?: () => void
}) {
  const { t } = useTranslation()
  const client = useQueryClient()
  const [target, setTarget] = useState(
    props.selection.target_vendor_id?.toString() ?? ''
  )
  const action = props.selection.action
  const vendorsQuery = useQuery({
    queryKey: vendorsQueryKeys.list(),
    queryFn: async () => {
      const response = await getVendors({ page_size: 1000 })
      if (!response.success) {
        throw createServerError(response, t('Failed to load vendors'))
      }
      return response
    },
    enabled: action !== 'delete',
  })
  const vendorOptions = (vendorsQuery.data?.data?.items ?? []).map(
    (vendor) => ({
      value: String(vendor.id),
      label: vendor.name,
      icon: getLobeIcon(vendor.icon, 18),
    })
  )
  if (action === 'assign') {
    vendorOptions.unshift({
      value: '0',
      label: t('No vendor'),
      icon: getLobeIcon('', 18),
    })
  }
  const request: VendorOperation = {
    ...props.selection,
    target_vendor_id: Number(target),
    vendor_ids:
      action === 'merge'
        ? props.selection.vendor_ids?.filter((id) => id !== Number(target))
        : props.selection.vendor_ids,
  }
  const preview = useMutation({
    mutationFn: () => previewVendorOperation(request),
  })
  const apply = useMutation({
    mutationFn: () =>
      applyVendorOperation({
        ...request,
        expected_version: preview.data?.version,
      }),
    onSuccess: async (result) => {
      await invalidateVendorData(client)
      toast.success(
        t(
          'Updated {{models}} model assignments and deleted {{vendors}} vendor records.',
          {
            models: result.updated_models.length,
            vendors: result.deleted_vendors.length,
          }
        )
      )
      props.onSuccess?.()
      props.onClose()
    },
  })
  let title = t('Change model vendor')
  if (action === 'merge') title = t('Merge vendors')
  if (action === 'delete') title = t('Delete vendors')
  const busy = preview.isPending || apply.isPending
  const valid =
    action === 'delete' ||
    (target !== '' &&
      (action !== 'merge' || Boolean(request.vendor_ids?.length)))
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) props.onClose()
      }}
      title={title}
      description={t(
        'Review the affected records before applying. Pricing, channels, and model names are preserved.'
      )}
      contentClassName='sm:max-w-4xl'
      contentHeight='min(75vh, 760px)'
      footer={
        <>
          <Button variant='outline' disabled={busy} onClick={props.onClose}>
            {t('Cancel')}
          </Button>
          {!preview.data && (
            <Button
              disabled={
                !valid || busy || (action !== 'delete' && vendorsQuery.isError)
              }
              onClick={() => preview.mutate()}
            >
              {t('Preview changes')}
            </Button>
          )}
          {preview.data && (
            <Button
              variant={action === 'assign' ? 'default' : 'destructive'}
              disabled={busy || apply.isError}
              onClick={() => apply.mutate()}
            >
              {busy ? t('Applying...') : t('Apply changes')}
            </Button>
          )}
        </>
      }
    >
      <div className='space-y-4'>
        {action !== 'delete' && (
          <div className='space-y-2'>
            <Label htmlFor='vendor-operation-target'>
              {action === 'merge' ? t('Keep this vendor') : t('Target vendor')}
            </Label>
            <Combobox
              id='vendor-operation-target'
              disabled={busy}
              options={vendorOptions}
              value={target}
              onValueChange={(value) => {
                if (busy) return
                setTarget(value ?? '')
                preview.reset()
                apply.reset()
              }}
              searchPlaceholder={t('Search vendors')}
              emptyText={t('No vendors found')}
            />
            {action === 'merge' && (
              <p className='text-muted-foreground text-sm'>
                {t(
                  'Source vendors will be deleted after their model assignments are moved. The target vendor’s details are preserved.'
                )}
              </p>
            )}
          </div>
        )}
        {action === 'delete' && (
          <p className='text-sm'>
            {t(
              'Delete {{count}} selected vendor records? Vendors with linked models cannot be deleted.',
              { count: request.vendor_ids?.length ?? 0 }
            )}
          </p>
        )}
        {action !== 'delete' && vendorsQuery.isError && (
          <ErrorState
            description={vendorErrorMessage(vendorsQuery.error)}
            onRetry={() => void vendorsQuery.refetch()}
          />
        )}
        {busy && <LoadingState />}
        {preview.isError && (
          <ErrorState
            description={vendorErrorMessage(preview.error)}
            onRetry={() => preview.mutate()}
          />
        )}
        {apply.isError && (
          <ErrorState
            description={vendorErrorMessage(apply.error)}
            action={
              <Button
                variant='outline'
                onClick={() => {
                  apply.reset()
                  preview.reset()
                  preview.mutate()
                }}
              >
                {t('Preview again')}
              </Button>
            }
          />
        )}
        {preview.data && (
          <>
            <div className='space-y-2 rounded-lg border p-3 text-sm'>
              <p>
                {t('Source vendors')}:{' '}
                {preview.data.sources.map((vendor) => vendor.name).join(', ') ||
                  t('No vendor')}
              </p>
              {action !== 'delete' && (
                <p>
                  {t('Target vendor')}:{' '}
                  {preview.data.target?.name || t('No vendor')}
                </p>
              )}
              <p>
                {t('{{count}} linked model records', {
                  count: preview.data.models.length,
                })}
              </p>
            </div>
            {action === 'delete' ? (
              <StaticDataTable
                data={preview.data.sources}
                columns={[
                  {
                    id: 'vendor',
                    header: t('Vendor'),
                    cell: (vendor) => vendor.name,
                  },
                  {
                    id: 'action',
                    header: t('Planned action'),
                    cell: () => t('Delete vendor record'),
                  },
                ]}
              />
            ) : (
              <StaticDataTable
                tableClassName='min-w-[480px]'
                data={preview.data.models}
                columns={[
                  {
                    id: 'model',
                    header: t('Model'),
                    cell: (model) => (
                      <span className='block max-w-60 font-mono break-all whitespace-normal'>
                        {model.model_name}
                      </span>
                    ),
                  },
                  {
                    id: 'source',
                    header: t('Current vendor'),
                    cell: (model) => model.vendor_name || t('No vendor'),
                  },
                  {
                    id: 'target',
                    header: t('Target vendor'),
                    cell: () => preview.data?.target?.name || t('No vendor'),
                  },
                ]}
              />
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
