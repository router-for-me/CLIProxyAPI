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
import { Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  formatCreemPrice,
  formatQuotaShort,
} from '@/features/wallet/lib/format'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray } from '../utils/json-validators'
import {
  CreemProductDialog,
  type CreemProductData,
} from './creem-product-dialog'

type CreemProductsVisualEditorProps = {
  value: string
  onChange: (value: string) => void
}

export function CreemProductsVisualEditor({
  value,
  onChange,
}: CreemProductsVisualEditorProps) {
  const { t } = useTranslation()
  const [searchText, setSearchText] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<CreemProductData | null>(null)

  const products = useMemo(() => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      validatorMessage: t('Creem products must be a JSON array'),
      context: 'creem products',
    })

    return parsed.filter(
      (item): item is CreemProductData =>
        typeof item === 'object' &&
        item !== null &&
        'name' in item &&
        'productId' in item &&
        'price' in item &&
        'quota' in item &&
        'currency' in item &&
        typeof item.name === 'string' &&
        typeof item.productId === 'string' &&
        typeof item.price === 'number' &&
        typeof item.quota === 'number' &&
        (item.currency === 'USD' || item.currency === 'EUR')
    )
  }, [value, t])

  const filteredProducts = useMemo(() => {
    if (!searchText) return products
    const lowerSearch = searchText.toLowerCase()
    return products.filter(
      (product) =>
        product.name.toLowerCase().includes(lowerSearch) ||
        product.productId.toLowerCase().includes(lowerSearch)
    )
  }, [products, searchText])

  const handleSave = (data: CreemProductData) => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      silent: true,
    })

    const updatedArray = [...parsed]

    if (editData) {
      const index = updatedArray.findIndex(
        (item): item is CreemProductData =>
          typeof item === 'object' &&
          item !== null &&
          'productId' in item &&
          item.productId === editData.productId
      )
      if (index !== -1) {
        updatedArray[index] = data
      } else {
        updatedArray.push(data)
      }
    } else {
      updatedArray.push(data)
    }

    onChange(JSON.stringify(updatedArray, null, 2))
  }

  const handleDelete = (product: CreemProductData) => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      silent: true,
    })

    const updatedArray = parsed.filter(
      (item) =>
        !(
          typeof item === 'object' &&
          item !== null &&
          'productId' in item &&
          item.productId === product.productId
        )
    )

    onChange(JSON.stringify(updatedArray, null, 2))
  }

  const handleEdit = (product: CreemProductData) => {
    setEditData(product)
    setDialogOpen(true)
  }

  const handleAdd = () => {
    setEditData(null)
    setDialogOpen(true)
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-col gap-3 sm:flex-row sm:items-center'>
        <div className='relative flex-1'>
          <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4' />
          <Input
            placeholder={t('Search products...')}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            className='pl-9'
          />
        </div>
        <Button
          type='button'
          onClick={(e) => {
            e.preventDefault()
            e.stopPropagation()
            handleAdd()
          }}
          className='flex-1 sm:flex-none'
        >
          <Plus className='h-4 w-4 sm:mr-2' />
          <span className='sm:inline'>{t('Add product')}</span>
        </Button>
      </div>

      {filteredProducts.length === 0 ? (
        <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
          {searchText
            ? t('No products match your search')
            : t('No products configured. Click "Add product" to get started.')}
        </div>
      ) : (
        <div className='rounded-md border'>
          {/* Desktop table view */}
          <StaticDataTable
            className='hidden rounded-none border-0 md:block'
            data={filteredProducts}
            getRowKey={(product) => product.productId}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (product) => product.name,
              },
              {
                id: 'product-id',
                header: t('Product ID'),
                cell: (product) => (
                  <code className='bg-muted rounded px-1.5 py-0.5 text-sm'>
                    {product.productId}
                  </code>
                ),
              },
              {
                id: 'price',
                header: t('Price'),
                cell: (product) => (
                  <span className='font-mono text-sm'>
                    {formatCreemPrice(product.price, product.currency)}
                  </span>
                ),
              },
              {
                id: 'quota',
                header: t('Quota'),
                cell: (product) => (
                  <span className='font-mono text-sm'>
                    {formatQuotaShort(product.quota)}
                  </span>
                ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (product) => (
                  <StaticRowActions
                    editLabel={t('Edit')}
                    deleteLabel={t('Delete')}
                    menuLabel={t('Open menu')}
                    onEdit={() => handleEdit(product)}
                    onDelete={() => handleDelete(product)}
                  />
                ),
              },
            ]}
          />

          {/* Mobile card view */}
          <div className='divide-y md:hidden'>
            {filteredProducts.map((product) => (
              <div key={product.productId} className='p-4'>
                <div className='mb-3 flex items-start justify-between'>
                  <div className='flex-1'>
                    <div className='mb-1 font-medium'>{product.name}</div>
                    <code className='bg-muted rounded px-1.5 py-0.5 text-xs'>
                      {product.productId}
                    </code>
                  </div>
                  <div className='flex gap-1'>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleEdit(product)
                      }}
                    >
                      <Pencil className='h-4 w-4' />
                    </Button>
                    <Button
                      type='button'
                      variant='ghost'
                      size='sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleDelete(product)
                      }}
                    >
                      <Trash2 className='h-4 w-4' />
                    </Button>
                  </div>
                </div>
                <div className='space-y-2 text-sm'>
                  <div className='flex items-center gap-2'>
                    <span className='text-muted-foreground min-w-16'>
                      {t('Price')}:
                    </span>
                    <span className='font-mono'>
                      {formatCreemPrice(product.price, product.currency)}
                    </span>
                  </div>
                  <div className='flex items-center gap-2'>
                    <span className='text-muted-foreground min-w-16'>
                      {t('Quota')}:
                    </span>
                    <span className='font-mono'>
                      {formatQuotaShort(product.quota)}
                    </span>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      <CreemProductDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={handleSave}
        editData={editData}
      />
    </div>
  )
}
