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
import { ChevronLeft, ChevronRight, Plus, Search } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty'
import { Input } from '@/components/ui/input'
import { useIsMobile } from '@/hooks/use-mobile'
import { createServerError } from '@/lib/server-error-message'

import { getMissingModels } from '../../api'
import { DEFAULT_PAGE_SIZE } from '../../constants'
import { modelsQueryKeys } from '../../lib'
import type { Model } from '../../types'
import { useModels } from '../models-provider'

type MissingModelsDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function MissingModelsDialog({
  open,
  onOpenChange,
}: MissingModelsDialogProps) {
  const { t } = useTranslation()
  const { setOpen, setCurrentRow } = useModels()
  const isMobile = useIsMobile()
  const [searchTerm, setSearchTerm] = useState('')
  const [currentPage, setCurrentPage] = useState(1)

  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: modelsQueryKeys.missing(),
    queryFn: async () => {
      const response = await getMissingModels()
      if (!response.success) {
        throw createServerError(response, t('Operation failed'))
      }
      return response
    },
    enabled: open,
  })

  const missingModels = useMemo(() => data?.data || [], [data?.data])
  const pageSize = DEFAULT_PAGE_SIZE

  const handleConfigureModel = (modelName: string) => {
    setCurrentRow({ model_name: modelName } as unknown as Model)
    setOpen('create-model')
  }

  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setSearchTerm('')

      setCurrentPage(1)
    }
  }, [open])

  const filteredModels = useMemo(() => {
    if (!searchTerm.trim()) {
      return missingModels
    }
    const keyword = searchTerm.toLowerCase().trim()
    return missingModels.filter((modelName) =>
      modelName.toLowerCase().includes(keyword)
    )
  }, [missingModels, searchTerm])

  const totalItems = filteredModels.length
  const totalPages =
    totalItems === 0 ? 1 : Math.ceil(totalItems / Math.max(1, pageSize))

  useEffect(() => {
    if (currentPage > totalPages) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setCurrentPage(Math.max(1, totalPages))
    }
  }, [currentPage, totalPages])

  const paginatedModels = useMemo(() => {
    const startIndex = (currentPage - 1) * pageSize
    const endIndex = startIndex + pageSize
    return filteredModels.slice(startIndex, endIndex)
  }, [filteredModels, currentPage, pageSize])

  const displayStart = totalItems === 0 ? 0 : (currentPage - 1) * pageSize + 1
  const displayEnd =
    totalItems === 0 ? 0 : Math.min(currentPage * pageSize, totalItems)
  const showPagination = totalItems > pageSize

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Missing Models')}
      description={t(
        'Models that are being used but not configured in the system'
      )}
      contentClassName='flex max-h-[min(85dvh,var(--dialog-available-height))] max-w-2xl flex-col gap-3 p-4'
      headerClassName='flex-shrink-0 text-start'
      contentHeight='min(74vh, 760px)'
      bodyClassName='space-y-4'
      initialFocus={!isMobile}
      footer={
        <Button variant='outline' onClick={() => setOpen('sync-wizard')}>
          {t('Sync missing metadata')}
        </Button>
      }
    >
      {isLoading && <LoadingState />}
      {isError && (
        <ErrorState
          description={error.message}
          onRetry={() => void refetch()}
        />
      )}
      {!isLoading && !isError && missingModels.length === 0 && (
        <div className='text-muted-foreground py-12 text-center'>
          <p>{t('No missing models found.')}</p>
          <p className='text-sm'>
            {t('All models in use are properly configured.')}
          </p>
        </div>
      )}
      {!isLoading && !isError && missingModels.length > 0 && (
        <div className='flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto'>
          <div className='flex flex-shrink-0 items-center justify-between gap-3'>
            <div className='text-muted-foreground text-sm whitespace-nowrap'>
              {t('Showing')} {displayStart}-{displayEnd} {t('of')} {totalItems}
            </div>
            <div className='relative w-48'>
              <Search className='text-muted-foreground pointer-events-none absolute top-1/2 left-3 h-4 w-4 -translate-y-1/2' />
              <Input
                value={searchTerm}
                onChange={(event) => {
                  setSearchTerm(event.target.value)
                  setCurrentPage(1)
                }}
                placeholder={t('Search models...')}
                className='pl-9'
                aria-label={t('Search missing models')}
              />
            </div>
          </div>

          {filteredModels.length === 0 ? (
            <Empty className='border'>
              <EmptyHeader>
                <EmptyMedia variant='icon'>
                  <Search className='h-5 w-5' />
                </EmptyMedia>
                <EmptyTitle>{t('No matches found')}</EmptyTitle>
                <EmptyDescription>
                  {t('Try adjusting your search to locate a missing model.')}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          ) : (
            <div className='flex-shrink-0 rounded-lg border'>
              <div className='divide-y'>
                {paginatedModels.map((modelName) => (
                  <div
                    key={modelName}
                    className='flex items-center justify-between gap-3 p-3'
                  >
                    <div className='min-w-0 flex-1'>
                      <StatusBadge
                        label={modelName}
                        variant='neutral'
                        copyText={modelName}
                      />
                    </div>
                    <Button
                      size='sm'
                      className='flex-shrink-0 gap-1'
                      onClick={() => handleConfigureModel(modelName)}
                    >
                      <Plus className='h-4 w-4' />
                      Configure
                    </Button>
                  </div>
                ))}
              </div>

              <div className='bg-muted/40 flex items-center justify-between border-t px-3 py-2 text-sm'>
                <div className='text-muted-foreground text-sm'>
                  {t('Page {{current}} of {{total}}', {
                    current: currentPage,
                    total: totalPages,
                  })}
                </div>
                {showPagination && (
                  <div className='flex items-center gap-2'>
                    <Button
                      variant='outline'
                      size='icon'
                      className='h-8 w-8'
                      onClick={() =>
                        setCurrentPage((prev) => Math.max(1, prev - 1))
                      }
                      disabled={currentPage === 1}
                      aria-label={t('Previous page')}
                    >
                      <ChevronLeft className='h-4 w-4' />
                    </Button>
                    <Button
                      variant='outline'
                      size='icon'
                      className='h-8 w-8'
                      onClick={() =>
                        setCurrentPage((prev) => Math.min(totalPages, prev + 1))
                      }
                      disabled={currentPage === totalPages}
                      aria-label={t('Next page')}
                    >
                      <ChevronRight className='h-4 w-4' />
                    </Button>
                  </div>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </Dialog>
  )
}
