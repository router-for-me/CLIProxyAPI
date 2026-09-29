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
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { formatNumber } from '@/lib/format'

import { formatCreemPrice } from '../../lib/format'
import type { CreemProduct } from '../../types'

interface CreemConfirmDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => void
  product: CreemProduct | null
  processing: boolean
}

export function CreemConfirmDialog({
  open,
  onOpenChange,
  onConfirm,
  product,
  processing,
}: CreemConfirmDialogProps) {
  const { t } = useTranslation()

  if (!product) return null

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('Confirm Creem Purchase')}
      description={t('Review your purchase details before proceeding.')}
      contentClassName='max-sm:w-[calc(100vw-1.5rem)] sm:max-w-[425px]'
      footerClassName='grid grid-cols-2 gap-2 sm:flex'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            variant='outline'
            onClick={() => onOpenChange(false)}
            disabled={processing}
          >
            {t('Cancel')}
          </Button>
          <Button onClick={onConfirm} disabled={processing}>
            {processing && <Loader2 className='mr-2 h-4 w-4 animate-spin' />}
            {t('Confirm Payment')}
          </Button>
        </>
      }
    >
      <div className='space-y-3 py-3 sm:space-y-4 sm:py-4'>
        <div className='flex items-center justify-between'>
          <span className='text-muted-foreground'>{t('Product')}</span>
          <span className='font-medium'>{product.name}</span>
        </div>
        <div className='flex items-center justify-between'>
          <span className='text-muted-foreground'>{t('Price')}</span>
          <span className='text-primary font-medium'>
            {formatCreemPrice(product.price, product.currency)}
          </span>
        </div>
        <div className='flex items-center justify-between'>
          <span className='text-muted-foreground'>{t('Quota')}</span>
          <span className='font-medium'>{formatNumber(product.quota)}</span>
        </div>
      </div>
    </Dialog>
  )
}
