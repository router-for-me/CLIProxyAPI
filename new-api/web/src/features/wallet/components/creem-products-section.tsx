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
import { useTranslation } from 'react-i18next'

import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { formatNumber } from '@/lib/format'

import { formatCreemPrice } from '../lib/format'
import type { CreemProduct } from '../types'

interface CreemProductsSectionProps {
  products: CreemProduct[]
  onProductSelect: (product: CreemProduct) => void
  loading?: boolean
}

export function CreemProductsSection({
  products,
  onProductSelect,
  loading,
}: CreemProductsSectionProps) {
  const { t } = useTranslation()

  if (loading) {
    return (
      <div className='grid grid-cols-2 gap-2 sm:grid-cols-2 sm:gap-3 md:grid-cols-3'>
        {Array.from({ length: 3 }).map((_, i) => (
          <Skeleton key={i} className='h-24 rounded-lg' />
        ))}
      </div>
    )
  }

  if (!Array.isArray(products) || products.length === 0) {
    return null
  }

  return (
    <div className='grid grid-cols-2 gap-2 sm:grid-cols-2 sm:gap-3 md:grid-cols-3'>
      {products.map((product) => (
        <Card
          key={product.productId}
          data-card-hover='false'
          className='cursor-pointer'
          onClick={() => onProductSelect(product)}
        >
          <CardContent className='p-3 text-center sm:p-4'>
            <div className='mb-2 text-lg font-medium'>{product.name}</div>
            <div className='text-muted-foreground mb-2 text-sm'>
              {t('Quota')}: {formatNumber(product.quota)}
            </div>
            <div className='text-primary text-lg font-semibold'>
              {formatCreemPrice(product.price, product.currency)}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
