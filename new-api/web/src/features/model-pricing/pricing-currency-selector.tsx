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
import { HelpCircle } from 'lucide-react'
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { usePricingPreferencesStore } from '@/stores/pricing-preferences-store'

import { isValidPricingCurrency, type PricingCurrency } from './currency'

export function PricingCurrencySelector(props: {
  siteCurrency: PricingCurrency | null
}) {
  const { t } = useTranslation()
  const id = useId()
  const preference = usePricingPreferencesStore((state) => state.currency)
  const setCurrency = usePricingPreferencesStore((state) => state.setCurrency)
  const available = isValidPricingCurrency(props.siteCurrency)
  const value = available && preference === 'site' ? 'site' : 'USD'
  const items = [{ value: 'USD', label: t('US dollar (USD)') }]
  if (props.siteCurrency) {
    items.push({
      value: 'site',
      label: t('Site currency ({{currency}})', {
        currency: props.siteCurrency.label,
      }),
    })
  }

  return (
    <Field className='mb-4 gap-2'>
      <div className='flex items-center gap-1'>
        <FieldLabel htmlFor={id}>{t('Pricing currency')}</FieldLabel>
        <Dialog
          title={t('About pricing currency')}
          contentClassName='sm:max-w-lg'
          trigger={
            <Button
              type='button'
              size='icon-sm'
              variant='ghost'
              aria-label={t('About pricing currency')}
            >
              <HelpCircle aria-hidden='true' />
            </Button>
          }
        >
          <p className='text-sm'>
            {t(
              'The system always bills in USD. Site currency makes entering and converting prices easier; amounts are converted to USD using the site exchange rate. Switching currencies does not change the actual price. Raw billing expressions always use USD.'
            )}
          </p>
          {available && props.siteCurrency && (
            <p className='mt-3 text-sm'>
              {t('Current exchange rate: 1 USD = {{rate}} {{currency}}', {
                rate: String(props.siteCurrency.exchangeRate),
                currency: props.siteCurrency.label,
              })}
            </p>
          )}
        </Dialog>
      </div>
      <Select
        items={items}
        value={value}
        onValueChange={(next) => {
          if (next === 'USD' || (next === 'site' && available)) {
            setCurrency(next)
          }
        }}
      >
        <SelectTrigger
          id={id}
          className='w-full sm:w-64'
          aria-describedby={
            props.siteCurrency && !available ? `${id}-error` : undefined
          }
        >
          <SelectValue />
        </SelectTrigger>
        <SelectContent alignItemWithTrigger={false}>
          {items.map((item) => (
            <SelectItem
              key={item.value}
              value={item.value}
              disabled={item.value === 'site' && !available}
            >
              {item.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {props.siteCurrency && !available && (
        <p id={`${id}-error`} className='text-muted-foreground text-xs'>
          {t('The site exchange rate is invalid. Prices are shown in USD.')}
        </p>
      )}
    </Field>
  )
}
