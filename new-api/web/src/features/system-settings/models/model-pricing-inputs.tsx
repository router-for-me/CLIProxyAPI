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
import { useId } from 'react'
import { useTranslation } from 'react-i18next'

import { InputGroup, InputGroupAddon } from '@/components/ui/input-group'
import {
  USD_PRICING_CURRENCY,
  type PricingCurrency,
} from '@/features/model-pricing/currency'
import { PricingAmountInput } from '@/features/model-pricing/pricing-amount-input'
import { cn } from '@/lib/utils'

import {
  SettingsControlGroup,
  SettingsSwitchField,
} from '../components/settings-form-layout'

export function PriceInput(props: {
  currency?: PricingCurrency
  id?: string
  'aria-label'?: string
  'aria-describedby'?: string
  value: string
  placeholder?: string
  disabled?: boolean
  onChange: (value: string) => void
}) {
  return (
    <InputGroup className='has-[[data-pricing-error]]:h-auto has-[[data-pricing-error]]:flex-wrap'>
      <InputGroupAddon>
        {(props.currency ?? USD_PRICING_CURRENCY).symbol}
      </InputGroupAddon>
      <PricingAmountInput
        grouped
        currency={props.currency}
        id={props.id}
        aria-label={props['aria-label']}
        aria-describedby={props['aria-describedby']}
        inputMode='decimal'
        value={props.value}
        placeholder={props.placeholder}
        disabled={props.disabled}
        onChange={props.onChange}
      />
      <InputGroupAddon align='inline-end'>
        {(props.currency ?? USD_PRICING_CURRENCY).symbol}/1M
      </InputGroupAddon>
    </InputGroup>
  )
}

export function PriceLane(props: {
  currency?: PricingCurrency
  title: string
  description: string
  placeholder: string
  value: string
  enabled: boolean
  disabled?: boolean
  compact?: boolean
  disabledReason?: string
  onEnabledChange: (checked: boolean) => void
  onChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const controlId = useId()
  const effectiveDisabled = props.disabled || !props.enabled

  return (
    <SettingsControlGroup
      className={cn(
        'space-y-3',
        props.compact && 'space-y-2 rounded-lg bg-transparent p-3',
        effectiveDisabled && 'opacity-75'
      )}
      data-disabled={effectiveDisabled || undefined}
    >
      <SettingsSwitchField
        controlId={controlId}
        className={props.compact ? 'py-0' : undefined}
        checked={props.enabled}
        disabled={props.disabled}
        onCheckedChange={props.onEnabledChange}
        label={props.title}
        description={props.disabledReason || props.description}
        aria-label={props.title}
      />
      <PriceInput
        currency={props.currency}
        aria-label={props.title}
        aria-describedby={`${controlId}-description`}
        value={props.value}
        placeholder={props.placeholder}
        disabled={effectiveDisabled}
        onChange={props.onChange}
      />
      {!props.compact && (
        <p className='text-muted-foreground text-xs'>
          {props.enabled
            ? t('{{currency}} price per 1M tokens.', {
                currency: (props.currency ?? USD_PRICING_CURRENCY).label,
              })
            : t('Disabled lanes are omitted on save.')}
        </p>
      )}
    </SettingsControlGroup>
  )
}
