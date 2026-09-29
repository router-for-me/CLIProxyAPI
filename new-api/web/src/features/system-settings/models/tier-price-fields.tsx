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
import { ChevronDown } from 'lucide-react'
import { useEffect, useId, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'
import type { PricingCurrency } from '@/features/model-pricing/currency'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import { BILLING_EXTRA_VARS } from '@/features/pricing/lib/billing-expr'
import type { VisualPrice } from '@/features/pricing/lib/billing-expression/visual'
import {
  CACHE_MODE_GENERIC,
  CACHE_MODE_TIMED,
  type CacheMode,
} from '@/features/pricing/lib/tier-expr'
import { cn } from '@/lib/utils'

const PRICE_VARS = BILLING_EXTRA_VARS.map((variable) => ({
  ...variable,
  key: variable.key as VisualPrice['variable'],
}))
const CACHE_PRICE_VARS = PRICE_VARS.filter(
  (variable) => variable.group === 'cache' && variable.key !== 'img_cr'
)
const MEDIA_PRICE_ORDER = ['img', 'img_cr', 'img_o', 'ai', 'ao']
const MEDIA_PRICE_VARS = PRICE_VARS.filter(
  (variable) => variable.group === 'media' || variable.key === 'img_cr'
).sort(
  (left, right) =>
    MEDIA_PRICE_ORDER.indexOf(left.key) - MEDIA_PRICE_ORDER.indexOf(right.key)
)
type PriceFieldProps = {
  currency: PricingCurrency
  label: string
  hint?: string
  value: number | string
  onChange: (next: string) => void
  included?: boolean
  onInclude?: (included: boolean) => void
  invalid?: boolean
}

function PriceField({
  label,
  hint,
  value,
  onChange,
  currency,
  included,
  onInclude,
  invalid,
}: PriceFieldProps) {
  const id = useId()
  const { t } = useTranslation()
  return (
    <div className='w-36 space-y-0.5'>
      <div className='flex items-center gap-1'>
        {onInclude && (
          <Checkbox
            aria-label={t('Include {{price}}', { price: label })}
            checked={included}
            onCheckedChange={(checked) => onInclude(checked === true)}
          />
        )}
        <Label htmlFor={id} className='text-muted-foreground text-xs'>
          {label}
        </Label>
      </div>
      <PricingAmountInput
        id={id}
        currency={currency}
        aria-label={label}
        value={value}
        onChange={onChange}
        disabled={included === false}
        aria-invalid={invalid || undefined}
        className='h-8 w-full'
      />
      {hint && <p className='text-muted-foreground text-xs'>{hint}</p>}
    </div>
  )
}

type TierPriceFieldsProps = {
  billingUnit?: 'token' | 'request'
  fixedPrice?: string
  onBillingUnitChange?: (unit: 'token' | 'request') => void
  onFixedPriceChange?: (value: string) => void
  currency: PricingCurrency
  prices: Partial<Record<VisualPrice['variable'], number | string>>
  onChange: (variable: VisualPrice['variable'], value: string) => void
  onInclude?: (variable: VisualPrice['variable'], included: boolean) => void
  cacheMode?: CacheMode
  onCacheModeChange?: (mode: CacheMode) => void
  invalidVariables?: string[]
}
export function TierPriceFields(props: TierPriceFieldsProps) {
  const { t } = useTranslation()
  const hasMediaPricing = MEDIA_PRICE_VARS.some((variable) =>
    props.onInclude
      ? props.prices[variable.key] !== undefined
      : Number(props.prices[variable.key] ?? 0) > 0
  )
  const [mediaOpen, setMediaOpen] = useState(hasMediaPricing)
  useEffect(() => {
    if (hasMediaPricing) setMediaOpen(true)
  }, [hasMediaPricing])
  const renderPriceVariable = (variable: {
    key: VisualPrice['variable']
    label: string
  }) => (
    <PriceField
      key={variable.key}
      currency={props.currency}
      label={
        variable.key === 'cc' && props.prices.cc1h !== undefined
          ? t('Cache Creation (5m)')
          : t(variable.label)
      }
      value={props.prices[variable.key] ?? 0}
      onChange={(value) => props.onChange(variable.key, value)}
      included={
        props.onInclude ? props.prices[variable.key] !== undefined : undefined
      }
      onInclude={
        props.onInclude
          ? (included) => props.onInclude?.(variable.key, included)
          : undefined
      }
      invalid={props.invalidVariables?.includes(variable.key)}
    />
  )
  const billingControl = props.onBillingUnitChange && (
    <Select
      items={[
        { value: 'token', label: t('Per token') },
        { value: 'request', label: t('Per-call') },
      ]}
      value={props.billingUnit ?? 'token'}
      onValueChange={(value) => {
        if (value === 'token' || value === 'request') {
          props.onBillingUnitChange?.(value)
        }
      }}
    >
      <SelectTrigger
        aria-label={t('Tier billing mode')}
        size='sm'
        className='w-full sm:w-48'
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent alignItemWithTrigger={false}>
        <SelectGroup>
          <SelectItem value='token'>{t('Per token')}</SelectItem>
          <SelectItem value='request'>{t('Per-call')}</SelectItem>
        </SelectGroup>
      </SelectContent>
    </Select>
  )
  if (props.billingUnit === 'request') {
    return (
      <>
        {billingControl}
        <PriceField
          currency={props.currency}
          label={t('Price per request')}
          value={props.fixedPrice ?? ''}
          onChange={(value) => props.onFixedPriceChange?.(value)}
          hint={`${props.currency.symbol}/${t('request')}`}
          invalid={props.invalidVariables?.includes('fixed')}
        />
      </>
    )
  }
  return (
    <>
      {billingControl}
      <div className='space-y-2'>
        <div className='flex items-center justify-between gap-3'>
          <Label className='text-sm font-semibold'>{t('Token prices')}</Label>
          <span className='bg-muted text-muted-foreground rounded-md px-2 py-1 text-xs'>
            {props.currency.symbol}/{t('1M token')}
          </span>
        </div>

        <div className='space-y-3'>
          <div className='flex flex-wrap gap-x-4 gap-y-2'>
            {renderPriceVariable({ key: 'p', label: 'Input price' })}
            {renderPriceVariable({ key: 'c', label: 'Output price' })}
          </div>

          <div className='space-y-2'>
            <div className='flex h-7 items-center'>
              {props.onCacheModeChange && (
                <Tabs
                  value={props.cacheMode}
                  onValueChange={(value) =>
                    value !== null &&
                    props.onCacheModeChange?.(value as CacheMode)
                  }
                >
                  <TabsList className='h-8'>
                    <TabsTrigger
                      value={CACHE_MODE_GENERIC}
                      className='px-2 text-xs'
                    >
                      {t('Generic cache')}
                    </TabsTrigger>
                    <TabsTrigger
                      value={CACHE_MODE_TIMED}
                      className='px-2 text-xs'
                    >
                      {t('Time-sliced cache (Claude)')}
                    </TabsTrigger>
                  </TabsList>
                </Tabs>
              )}
            </div>
            <div className='flex flex-wrap gap-x-4 gap-y-2'>
              {CACHE_PRICE_VARS.map((variable) => {
                if (
                  variable.key === 'cc1h' &&
                  props.onCacheModeChange &&
                  props.cacheMode !== CACHE_MODE_TIMED
                ) {
                  return null
                }
                return renderPriceVariable(variable)
              })}
            </div>
          </div>
        </div>
      </div>

      {/* Media prices */}
      <div className='space-y-1.5'>
        <Button
          type='button'
          variant='ghost'
          size='sm'
          className='h-7 px-2 text-xs'
          onClick={() => setMediaOpen((prev) => !prev)}
        >
          <ChevronDown
            className={cn(
              'mr-1 h-3 w-3 transition-transform',
              mediaOpen && 'rotate-180'
            )}
          />
          {t('Media pricing')}
        </Button>
        {mediaOpen && (
          <div className='flex flex-wrap gap-x-4 gap-y-2'>
            {MEDIA_PRICE_VARS.map(renderPriceVariable)}
          </div>
        )}
      </div>
    </>
  )
}
