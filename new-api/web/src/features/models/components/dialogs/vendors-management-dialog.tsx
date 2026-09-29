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
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useDebounce } from '@/hooks/use-debounce'
import { createServerError } from '@/lib/server-error-message'

import { searchVendors } from '../../api'
import { vendorsQueryKeys } from '../../lib'
import type { Vendor } from '../../types'
import { VendorMutateDialog } from './vendor-mutate-dialog'

export function VendorsManagementDialog(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useTranslation()
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [editing, setEditing] = useState(false)
  const [vendor, setVendor] = useState<Vendor | null>(null)
  const keyword = useDebounce(search, 250)
  const query = useQuery({
    queryKey: vendorsQueryKeys.list({ keyword, p: page }),
    queryFn: async () => {
      const response = await searchVendors({ keyword, p: page, page_size: 20 })
      if (!response.success || !response.data) {
        throw createServerError(response, t('Failed to load vendors'))
      }
      return response.data
    },
    enabled: props.open,
  })
  useEffect(() => {
    if (props.open) {
      setSearch('')
      setPage(1)
    }
  }, [props.open])
  return (
    <>
      <Dialog
        open={props.open}
        onOpenChange={props.onOpenChange}
        title={t('Manage Vendors')}
        description={t('Search, add, and edit model vendors.')}
        contentClassName='sm:max-w-3xl'
        footer={
          <>
            <Button variant='outline' onClick={() => props.onOpenChange(false)}>
              {t('Close')}
            </Button>
            <Button
              onClick={() => {
                setVendor(null)
                setEditing(true)
              }}
            >
              {t('Add Vendor')}
            </Button>
          </>
        }
      >
        <div className='space-y-4'>
          <Input
            aria-label={t('Search vendors')}
            placeholder={t('Search vendors')}
            value={search}
            onChange={(event) => {
              setSearch(event.target.value)
              setPage(1)
            }}
          />
          {query.isPending ? <LoadingState /> : null}
          {query.isError && (
            <ErrorState
              description={query.error.message}
              onRetry={() => void query.refetch()}
            />
          )}
          {query.data && !query.isError && (
            <StaticDataTable
              data={query.data.items}
              columns={[
                { id: 'name', header: t('Vendor'), cell: (item) => item.name },
                {
                  id: 'description',
                  header: t('Description'),
                  cell: (item) => (
                    <span className='line-clamp-2'>
                      {item.description || '—'}
                    </span>
                  ),
                },
                {
                  id: 'actions',
                  header: t('Actions'),
                  cell: (item) => (
                    <Button
                      variant='ghost'
                      size='sm'
                      onClick={() => {
                        setVendor(item)
                        setEditing(true)
                      }}
                    >
                      {t('Edit')}
                    </Button>
                  ),
                },
              ]}
            />
          )}
          <div className='flex items-center justify-end gap-3'>
            <Button
              variant='outline'
              size='sm'
              disabled={page === 1}
              onClick={() => setPage(page - 1)}
            >
              {t('Previous')}
            </Button>
            <span className='text-sm'>{page}</span>
            <Button
              variant='outline'
              size='sm'
              disabled={page * 20 >= (query.data?.total ?? 0)}
              onClick={() => setPage(page + 1)}
            >
              {t('Next')}
            </Button>
          </div>
        </div>
      </Dialog>
      <VendorMutateDialog
        open={editing}
        onOpenChange={setEditing}
        currentVendor={vendor}
      />
    </>
  )
}
