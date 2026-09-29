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

import { CopyButton } from '@/components/copy-button'
import { StatusBadge } from '@/components/status-badge'
import { Checkbox } from '@/components/ui/checkbox'
import { cn } from '@/lib/utils'

import type { PricingSyncValues } from '../types'
import {
  getSyncPriceKind,
  getSyncPriceLines,
  getSyncExpressionPricing,
  getUpstreamDisplayName,
  sameSyncPrice,
  useSyncPriceSelection,
} from './upstream-ratio-sync-helpers'
import type { PricingSyncRow } from './upstream-ratio-sync-table'

export function SyncPriceCell(props: {
  values: PricingSyncValues
  compareTo?: PricingSyncValues
}) {
  const { t } = useTranslation()
  const kind = getSyncPriceKind(props.values)
  if (kind === 'unset') {
    return <span className='text-muted-foreground'>{t('Unset price')}</span>
  }
  if (kind === 'expression') {
    const parsed = getSyncExpressionPricing(String(props.values.billing_expr), t)
    return (
      <div className='min-w-0 flex-1 space-y-1'>
        <div className='flex items-center gap-2'>
          <span className='text-muted-foreground text-xs!'>
            {t('Expression pricing')}
          </span>
          <CopyButton
            value={String(props.values.billing_expr)}
            size='icon'
            className='size-6'
            aria-label={t('Copy billing expression')}
          />
        </div>
        {parsed ? (
          <div className='space-y-2'>
            {parsed.tiers.map((tier, index) => (
              <div key={`${tier.label}-${index}`} className='space-y-1'>
                {parsed.tiers.length > 1 && (
                  <div className='text-muted-foreground text-xs!'>
                    {tier.condition || tier.label || t('Default')}
                  </div>
                )}
                <SyncPriceMetrics lines={tier.lines} />
              </div>
            ))}
            {parsed.requestRuleExpr && (
              <div className='text-muted-foreground text-xs! break-all'>
                {t('Includes request rules')}: {parsed.requestRuleExpr}
              </div>
            )}
          </div>
        ) : <code className='block text-xs! leading-relaxed break-all whitespace-pre-wrap'>{highlightExprDiff(String(props.values.billing_expr), props.compareTo?.billing_expr)}</code>}
      </div>
    )
  }
  const lines = getSyncPriceLines(props.values, t)
  if (kind === 'request') {
    return (
      <span className='whitespace-nowrap'>
        <span className='font-medium tabular-nums'>{lines[0].value}</span>
        <span className='text-muted-foreground'> / {t('request')}</span>
      </span>
    )
  }
  return <SyncPriceMetrics lines={lines} />
}

// Positional word diff: only meaningful when both expressions share one shape.
function highlightExprDiff(expr: string, base: unknown) {
  const words = [...expr.matchAll(/\S+|\s+/g)]
  const baseWords = typeof base === 'string' ? base.match(/\S+|\s+/g) : null
  if (!baseWords || baseWords.length !== words.length) return expr
  return words.map((word, i) =>
    word[0] === baseWords[i] ? (
      word[0]
    ) : (
      <mark key={word.index} className='rounded-sm bg-amber-500/25 text-inherit'>
        {word[0]}
      </mark>
    )
  )
}

function SyncPriceMetrics(props: { lines: Array<{ label: string; value: string }> }) {
  return (
    <dl className='flex min-w-0 flex-1 flex-wrap gap-x-4 gap-y-2'>
      {props.lines.map((line) => (
        <div key={line.label} className='min-w-0'>
          <dt className='text-muted-foreground text-xs! leading-4'>
            {line.label}
          </dt>
          <dd className='leading-5 font-medium tabular-nums'>{line.value}</dd>
        </div>
      ))}
    </dl>
  )
}

export function SyncSourceHeader(props: { source: string }) {
  const { t } = useTranslation()
  const context = useSyncPriceSelection()
  const state = context.bulkStates[props.source]
  const count = state?.selections.length ?? 0
  const selected = state?.selectedModels.length ?? 0
  const name = getUpstreamDisplayName(props.source, t)
  return (
    <div className='flex min-w-0 items-center gap-2'>
      <Checkbox
        checked={count > 0 && count === selected}
        indeterminate={selected > 0 && selected < count}
        disabled={context.isDisabled || count === 0}
        aria-label={t('Select all prices from {{source}}', { source: name })}
        onCheckedChange={(checked) =>
          checked
            ? context.onSelectPrices(state.selections)
            : context.onUnselectPrices(state.selectedModels)
        }
      />
      <span className='min-w-0 flex-1 truncate' title={name}>
        {name}
      </span>
      <span className='text-muted-foreground text-xs tabular-nums'>
        {selected}/{count}
      </span>
    </div>
  )
}

export function SyncSourcePriceCell(props: {
  row: PricingSyncRow
  source: string
}) {
  const { t } = useTranslation()
  const context = useSyncPriceSelection()
  const model = props.row.model
  const values = props.row.prices.upstreams[props.source]
  if (!values) return <span className='text-muted-foreground'>—</span>
  const same = sameSyncPrice(values, props.row.prices.current)
  const confident = Object.values(props.row.differences ?? {}).every(
    (diff) => diff?.confidence?.[props.source] !== false
  )
  return (
    <div
      className={cn(
        'flex min-h-10 min-w-0 items-center gap-3 rounded-md px-2 py-1 -mx-2',
        context.selectedSources[model] === props.source && 'bg-primary/8'
      )}
    >
      {same ? (
        <StatusBadge
          label={t('Same as Local')}
          variant='neutral'
          size='sm'
          copyable={false}
        />
      ) : (
        <div className='flex shrink-0 items-center gap-2'>
          <Checkbox
            checked={context.selectedSources[model] === props.source}
            disabled={context.isDisabled}
            aria-label={t('Select price for {{model}} from {{source}}', {
              model,
              source: getUpstreamDisplayName(props.source, t),
            })}
            onCheckedChange={(checked) =>
              checked
                ? context.onSelectPrices([{ model, source: props.source }])
                : context.onUnselectPrices([model])
            }
          />
          {!confident && (
            <StatusBadge
              label={t('Verify upstream pricing')}
              variant='warning'
              size='sm'
              copyable={false}
            />
          )}
        </div>
      )}
      <SyncPriceCell values={values} compareTo={props.row.prices.current} />
    </div>
  )
}
