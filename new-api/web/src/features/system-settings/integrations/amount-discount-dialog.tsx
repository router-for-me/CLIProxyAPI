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
import { zodResolver } from '@hookform/resolvers/zod'
import { useEffect, useMemo } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import * as z from 'zod'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'

const createAmountDiscountDialogSchema = (t: (key: string) => string) =>
  z.object({
    amount: z
      .number()
      .positive(t('Amount must be greater than 0'))
      .int(t('Amount must be a whole number')),
    discountRate: z
      .number()
      .positive(t('Discount rate must be greater than 0'))
      .max(1, t('Discount rate must be ≤ 1')),
  })

type AmountDiscountDialogFormValues = z.infer<
  ReturnType<typeof createAmountDiscountDialogSchema>
>

const AMOUNT_DISCOUNT_FORM_ID = 'amount-discount-form'

export type AmountDiscountData = {
  amount: number
  discountRate: number
}

type AmountDiscountDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  onSave: (data: AmountDiscountData) => void
  editData?: AmountDiscountData | null
}

export function AmountDiscountDialog({
  open,
  onOpenChange,
  onSave,
  editData,
}: AmountDiscountDialogProps) {
  const { t } = useTranslation()
  const isEditMode = !!editData
  const amountDiscountDialogSchema = createAmountDiscountDialogSchema(t)

  const form = useForm<AmountDiscountDialogFormValues>({
    resolver: zodResolver(amountDiscountDialogSchema),
    defaultValues: {
      amount: 0,
      discountRate: 1,
    },
  })

  const discountRate = form.watch('discountRate')

  const discountPercentage = useMemo(() => {
    if (!discountRate || discountRate >= 1) return 0
    return Math.round((1 - discountRate) * 100)
  }, [discountRate])

  useEffect(() => {
    if (editData) {
      form.reset(editData)
    } else {
      form.reset({
        amount: 0,
        discountRate: 1,
      })
    }
  }, [editData, form, open])

  const handleSubmit = (values: AmountDiscountDialogFormValues) => {
    onSave({
      amount: values.amount,
      discountRate: values.discountRate,
    })
    form.reset()
    onOpenChange(false)
  }

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={isEditMode ? t('Edit discount tier') : t('Add discount tier')}
      description={t(
        'Set a discount rate for a specific recharge amount threshold.'
      )}
      contentClassName='sm:max-w-[500px]'
      contentHeight='auto'
      bodyClassName='space-y-4'
      footer={
        <>
          <Button
            type='button'
            variant='outline'
            onClick={() => onOpenChange(false)}
          >
            {t('Cancel')}
          </Button>
          <Button type='submit' form={AMOUNT_DISCOUNT_FORM_ID}>
            {isEditMode ? t('Update') : t('Add')}
          </Button>
        </>
      }
    >
      <Form {...form}>
        <form
          id={AMOUNT_DISCOUNT_FORM_ID}
          onSubmit={form.handleSubmit(handleSubmit)}
          className='space-y-4'
        >
          <FormField
            control={form.control}
            name='amount'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Recharge Amount (USD)')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    step='1'
                    min='1'
                    placeholder={t('e.g., 100')}
                    {...field}
                    onChange={(e) =>
                      field.onChange(parseInt(e.target.value) || 0)
                    }
                    disabled={isEditMode}
                  />
                </FormControl>
                <FormDescription>
                  {isEditMode
                    ? t('Amount cannot be changed when editing.')
                    : t(
                        'Minimum recharge amount to qualify for this discount.'
                      )}
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />

          <FormField
            control={form.control}
            name='discountRate'
            render={({ field }) => (
              <FormItem>
                <FormLabel>{t('Discount Rate')}</FormLabel>
                <FormControl>
                  <Input
                    type='number'
                    step='0.01'
                    min='0.01'
                    max='1'
                    placeholder={t('e.g., 0.95')}
                    {...field}
                    onChange={(e) =>
                      field.onChange(parseFloat(e.target.value) || 0)
                    }
                  />
                </FormControl>
                <FormDescription>
                  {t('Final price multiplier (0.95 = 5% discount')}
                  {discountPercentage > 0 && (
                    <span className='ml-1 font-medium text-green-600 dark:text-green-400'>
                      = {discountPercentage}
                      {t('% off')}
                    </span>
                  )}
                  )
                </FormDescription>
                <FormMessage />
              </FormItem>
            )}
          />
        </form>
      </Form>
    </Dialog>
  )
}
