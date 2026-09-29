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
import { Lightbulb, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { useState, useMemo } from 'react'
import { useTranslation } from 'react-i18next'

import { StaticDataTable } from '@/components/data-table/static/static-data-table'
import { StaticRowActions } from '@/components/data-table/static/static-row-actions'
import { ReactIconByName } from '@/components/react-icon-by-name'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'

import { safeJsonParseWithValidation } from '../utils/json-parser'
import { isArray } from '../utils/json-validators'
import {
  PaymentMethodDialog,
  type PaymentMethodData,
} from './payment-method-dialog'

type PaymentMethodsVisualEditorProps = {
  value: string
  onChange: (value: string) => void
}

const PAYMENT_TYPE_ICON_NAMES: Record<string, string> = {
  alipay: 'SiAlipay',
  stripe: 'SiStripe',
  waffo_pancake: 'LuCreditCard',
  wxpay: 'SiWechat',
}

function getDefaultIconName(type: string) {
  return PAYMENT_TYPE_ICON_NAMES[type] ?? ''
}

function getEffectiveIconName(method: PaymentMethodData) {
  return method.icon || getDefaultIconName(method.type)
}

export function PaymentMethodsVisualEditor({
  value,
  onChange,
}: PaymentMethodsVisualEditorProps) {
  const { t } = useTranslation()
  const paymentTemplates = [
    {
      name: t('Epay Alipay'),
      template: {
        icon: getDefaultIconName('alipay'),
        name: '支付宝',
        type: 'alipay',
      },
    },
    {
      name: t('Epay WeChat Pay'),
      template: {
        icon: getDefaultIconName('wxpay'),
        name: '微信',
        type: 'wxpay',
      },
    },
    {
      name: t('Stripe'),
      template: {
        icon: getDefaultIconName('stripe'),
        min_topup: '10',
        name: 'Stripe',
        type: 'stripe',
      },
    },
    {
      name: 'Waffo Pancake',
      template: {
        icon: getDefaultIconName('waffo_pancake'),
        name: 'Waffo Pancake',
        type: 'waffo_pancake',
      },
    },
    {
      name: t('Custom Epay method'),
      template: {
        icon: 'LuCreditCard',
        min_topup: '50',
        name: '自定义1',
        type: 'custom1',
      },
    },
  ]
  const [searchText, setSearchText] = useState('')
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editData, setEditData] = useState<PaymentMethodData | null>(null)

  const paymentMethods = useMemo(() => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      validatorMessage: 'Payment methods must be a JSON array',
      context: 'payment methods',
    })

    return parsed.filter(
      (item): item is PaymentMethodData =>
        typeof item === 'object' &&
        item !== null &&
        'name' in item &&
        'type' in item &&
        typeof item.name === 'string' &&
        typeof item.type === 'string' &&
        (!('icon' in item) || typeof item.icon === 'string') &&
        (!('min_topup' in item) || typeof item.min_topup === 'string') &&
        (!('color' in item) || typeof item.color === 'string')
    )
  }, [value])

  const filteredMethods = useMemo(() => {
    if (!searchText) return paymentMethods
    const lowerSearch = searchText.toLowerCase()
    return paymentMethods.filter(
      (method) =>
        method.name.toLowerCase().includes(lowerSearch) ||
        method.type.toLowerCase().includes(lowerSearch) ||
        getEffectiveIconName(method).toLowerCase().includes(lowerSearch)
    )
  }, [paymentMethods, searchText])

  const handleSave = (data: PaymentMethodData) => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      silent: true,
    })

    const updatedArray = [...parsed]

    if (editData) {
      const index = updatedArray.findIndex(
        (item): item is PaymentMethodData =>
          typeof item === 'object' &&
          item !== null &&
          'name' in item &&
          'type' in item &&
          item.name === editData.name &&
          item.type === editData.type
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

  const handleDelete = (method: PaymentMethodData) => {
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
          'name' in item &&
          'type' in item &&
          item.name === method.name &&
          item.type === method.type
        )
    )

    onChange(JSON.stringify(updatedArray, null, 2))
  }

  const handleEdit = (method: PaymentMethodData) => {
    setEditData(method)
    setDialogOpen(true)
  }

  const handleAdd = () => {
    setEditData(null)
    setDialogOpen(true)
  }

  const handleInsertTemplate = (template: PaymentMethodData) => {
    const parsed = safeJsonParseWithValidation<unknown[]>(value, {
      fallback: [],
      validator: isArray,
      silent: true,
    })

    // Check if template already exists
    const exists = parsed.some(
      (item) =>
        typeof item === 'object' &&
        item !== null &&
        'type' in item &&
        'name' in item &&
        item.type === template.type &&
        item.name === template.name
    )

    if (!exists) {
      parsed.push(template)
      onChange(JSON.stringify(parsed, null, 2))
    }
  }

  return (
    <div className='space-y-4'>
      <div className='flex flex-col gap-3 sm:flex-row sm:items-center'>
        <div className='relative flex-1'>
          <Search className='text-muted-foreground absolute top-2.5 left-2.5 h-4 w-4' />
          <Input
            placeholder={t('Search payment methods...')}
            value={searchText}
            onChange={(e) => setSearchText(e.target.value)}
            className='pl-9'
          />
        </div>
        <div className='flex gap-2'>
          <Popover>
            <PopoverTrigger
              render={
                <Button variant='outline' className='flex-1 sm:flex-none' />
              }
            >
              <Lightbulb className='h-4 w-4 sm:mr-2' />
              <span className='sm:inline'>{t('Templates')}</span>
            </PopoverTrigger>
            <PopoverContent className='w-60'>
              <div className='space-y-2'>
                <p className='text-muted-foreground text-xs'>
                  {t('Quick insert payment entries')}
                </p>
                <div className='space-y-1'>
                  {paymentTemplates.map((item) => (
                    <Button
                      key={item.name}
                      type='button'
                      variant='ghost'
                      className='w-full justify-start text-sm'
                      onClick={(e) => {
                        e.preventDefault()
                        e.stopPropagation()
                        handleInsertTemplate(item.template)
                      }}
                    >
                      <Plus className='mr-2 h-3 w-3' />
                      {item.name}
                    </Button>
                  ))}
                </div>
              </div>
            </PopoverContent>
          </Popover>
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
            <span className='sm:inline'>{t('Add method')}</span>
          </Button>
        </div>
      </div>

      {filteredMethods.length === 0 ? (
        <div className='text-muted-foreground rounded-lg border border-dashed p-8 text-center text-sm'>
          {searchText
            ? t('No payment methods match your search')
            : t(
                'No payment methods configured. Click "Add method" or use templates to get started.'
              )}
        </div>
      ) : (
        <div className='rounded-md border'>
          {/* Desktop table view */}
          <StaticDataTable
            className='hidden rounded-none border-0 md:block'
            data={filteredMethods}
            getRowKey={(method, index) => `${method.type}-${index}`}
            columns={[
              {
                id: 'name',
                header: t('Name'),
                cellClassName: 'font-medium',
                cell: (method) => method.name,
              },
              {
                id: 'type',
                header: t('Payment type key'),
                cell: (method) => (
                  <code className='bg-muted rounded px-1.5 py-0.5 text-sm'>
                    {method.type}
                  </code>
                ),
              },
              {
                id: 'icon',
                header: t('Icon'),
                cell: (method) => {
                  const iconName = getEffectiveIconName(method)

                  return iconName ? (
                    <div className='flex items-center gap-2'>
                      <ReactIconByName
                        name={iconName}
                        className='text-muted-foreground size-5 shrink-0'
                        title={iconName}
                      />
                      <span className='text-muted-foreground truncate font-mono text-sm'>
                        {iconName}
                      </span>
                    </div>
                  ) : (
                    <span className='text-muted-foreground text-sm'>—</span>
                  )
                },
              },
              {
                id: 'min-top-up',
                header: t('Min Top-up'),
                cell: (method) =>
                  method.min_topup ? (
                    <span className='font-mono text-sm'>
                      {method.min_topup}
                    </span>
                  ) : (
                    <span className='text-muted-foreground text-sm'>—</span>
                  ),
              },
              {
                id: 'actions',
                header: t('Actions'),
                className: 'text-right',
                cellClassName: 'text-right',
                cell: (method) => (
                  <StaticRowActions
                    editLabel={t('Edit')}
                    deleteLabel={t('Delete')}
                    menuLabel={t('Open menu')}
                    onEdit={() => handleEdit(method)}
                    onDelete={() => handleDelete(method)}
                  />
                ),
              },
            ]}
          />

          {/* Mobile card view */}
          <div className='divide-y md:hidden'>
            {filteredMethods.map((method) => {
              const iconName = getEffectiveIconName(method)
              const methodKey = [
                method.type,
                method.name,
                method.icon,
                method.min_topup,
                method.color,
              ]
                .filter(Boolean)
                .join('-')

              return (
                <div key={methodKey} className='p-4'>
                  <div className='mb-3 flex items-start justify-between'>
                    <div className='flex-1'>
                      <div className='mb-1 font-medium'>{method.name}</div>
                      <code className='bg-muted rounded px-1.5 py-0.5 text-xs'>
                        {method.type}
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
                          handleEdit(method)
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
                          handleDelete(method)
                        }}
                      >
                        <Trash2 className='h-4 w-4' />
                      </Button>
                    </div>
                  </div>
                  <div className='space-y-2 text-sm'>
                    <div className='flex items-center gap-2'>
                      <span className='text-muted-foreground min-w-20'>
                        {t('Icon')}
                      </span>
                      {iconName ? (
                        <div className='flex min-w-0 items-center gap-2'>
                          <ReactIconByName
                            name={iconName}
                            className='text-muted-foreground size-5 shrink-0'
                            title={iconName}
                          />
                          <span className='text-muted-foreground truncate font-mono text-xs'>
                            {iconName}
                          </span>
                        </div>
                      ) : (
                        <span className='text-muted-foreground text-xs'>—</span>
                      )}
                    </div>
                    {method.min_topup && (
                      <div className='flex items-center gap-2'>
                        <span className='text-muted-foreground min-w-20'>
                          {t('Min Top-up:')}
                        </span>
                        <span className='font-mono'>{method.min_topup}</span>
                      </div>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      )}

      <PaymentMethodDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        onSave={handleSave}
        editData={editData}
      />
    </div>
  )
}
