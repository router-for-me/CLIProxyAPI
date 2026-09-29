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

import { QuotaDetailsPopover } from '@/components/quota-details-popover'
import { Progress } from '@/components/ui/progress'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency, getCurrencyDisplay } from '@/lib/currency'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { API_KEY_STATUS } from '../constants'
import type { ApiKey } from '../types'

type ApiKeyQuotaCellProps = {
  apiKey: ApiKey
  now: number
  variant?: 'table' | 'card'
}

export function ApiKeyQuotaCell(props: ApiKeyQuotaCellProps) {
  const { t, i18n } = useTranslation()
  useSystemConfigStore((state) => state.config.currency)
  const { meta: currency } = getCurrencyDisplay()
  const quotaUnit = currency.kind === 'tokens' ? t('Tokens') : currency.symbol
  const used = props.apiKey.used_quota
  const remaining = props.apiKey.remain_quota
  const total = used + remaining
  const hasProgress = !props.apiKey.unlimited_quota && total > 0
  const percentage = hasProgress
    ? Math.min(100, Math.max(0, (remaining / total) * 100))
    : 0
  const formattedUsed = formatQuotaWithCurrency(used, { showSymbol: false })
  const formattedRemaining = formatQuotaWithCurrency(remaining, {
    showSymbol: false,
  })
  const formattedTotal = formatQuotaWithCurrency(total, { showSymbol: false })
  const formattedPercentage = new Intl.NumberFormat(
    toIntlLocale(i18n.resolvedLanguage || i18n.language),
    { maximumFractionDigits: 1 }
  ).format(percentage)
  const isInactive =
    props.apiKey.status !== API_KEY_STATUS.ENABLED ||
    remaining <= 0 ||
    (props.apiKey.expired_time !== -1 &&
      props.apiKey.expired_time * 1000 <= props.now)
  let progressColor = 'text-emerald-500'
  if (isInactive) progressColor = 'text-muted-foreground/60'
  else if (percentage <= 10) progressColor = 'text-rose-500'
  else if (percentage <= 30) progressColor = 'text-amber-500'
  const usageDescription = `${t('Used amount')} ${formattedUsed}`
  const remainingDescription = hasProgress
    ? `${t('Remaining')} ${formattedRemaining}; ${t('Remaining percentage')} ${formattedPercentage}%`
    : `${t('Remaining')} ${formattedRemaining}`
  const triggerLabel = props.apiKey.unlimited_quota
    ? `${t('Unlimited')}; ${usageDescription}`
    : `${remainingDescription}; ${usageDescription}`

  const details = []
  if (!props.apiKey.unlimited_quota) {
    details.push({ label: t('Remaining'), value: formattedRemaining })
  }
  details.push({ label: t('Used amount'), value: formattedUsed })
  if (!props.apiKey.unlimited_quota) {
    details.push({ label: t('Current total quota'), value: formattedTotal })
  }
  if (hasProgress) {
    details.push({
      label: t('Remaining percentage'),
      value: `${formattedPercentage}%`,
    })
  }

  return (
    <QuotaDetailsPopover
      title={`${t('Quota')} (${quotaUnit})`}
      triggerLabel={
        props.variant === 'card'
          ? `${t('Quota')} (${quotaUnit}); ${triggerLabel}`
          : triggerLabel
      }
      details={details}
      description={
        props.apiKey.unlimited_quota
          ? t(
              'This API key has no quota limit. Requests still require available wallet or subscription quota.'
            )
          : t(
              'Total = used + remaining. It is not an initial allocation or a periodic budget; changing the remaining quota changes the total and percentage.'
            )
      }
      className={
        props.variant === 'card' ? 'space-y-2.5' : 'max-w-45 space-y-1.5'
      }
      triggerClassName={props.variant === 'card' ? 'py-0' : undefined}
      afterTrigger={
        !props.apiKey.unlimited_quota && (
          <Progress
            value={percentage}
            aria-label={t('Remaining percentage')}
            className={cn(
              'w-full [&_[data-slot=progress-indicator]]:bg-current',
              progressColor
            )}
          />
        )
      }
    >
      <span
        data-slot='api-key-quota-values'
        className={cn(
          'grid w-full min-w-0 items-baseline gap-x-2 gap-y-1 text-sm',
          props.variant === 'card'
            ? 'grid-cols-[auto_minmax(0,1fr)]'
            : 'grid-cols-2'
        )}
      >
        {props.variant === 'card' && (
          <span className='text-muted-foreground'>
            {t('Remaining')}
            <span className='ml-1'>({quotaUnit})</span>
          </span>
        )}
        <span
          className={cn(
            'min-w-0 truncate',
            props.variant === 'card'
              ? 'text-right text-sm leading-5 font-normal'
              : 'text-left font-medium',
            !props.apiKey.unlimited_quota && 'tabular-nums',
            !props.apiKey.unlimited_quota &&
              remaining < 0 &&
              'text-destructive',
            remaining === 0 &&
              !props.apiKey.unlimited_quota &&
              'text-muted-foreground'
          )}
        >
          {props.apiKey.unlimited_quota ? t('Unlimited') : formattedRemaining}
        </span>
        {props.variant === 'card' && (
          <span className='text-muted-foreground'>{t('Used amount')}</span>
        )}
        <span
          className={cn(
            'text-muted-foreground min-w-0 truncate text-right tabular-nums',
            props.variant === 'card' && 'text-sm leading-5 font-normal'
          )}
        >
          {formattedUsed}
        </span>
      </span>
    </QuotaDetailsPopover>
  )
}
