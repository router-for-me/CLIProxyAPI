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

import { getLobeIcon } from '@/lib/lobe-icon'

import { formatTokens } from '../lib/format'
import type { ModelRanking } from '../types'
import { ModelLink, VendorLink } from './entity-links'
import { GrowthText } from './growth-text'

type ModelLeaderboardProps = {
  rows: ModelRanking[]
  /** Density variant. `compact` is used inside per-category sections; the
   * default fits the larger overall "Top Models" section. */
  variant?: 'default' | 'compact'
  /** Optional cap (rows beyond this are dropped). */
  limit?: number
}

/**
 * Two-column model leaderboard list: "rank · model
 * (with vendor below) · tokens (with growth below)" rendering. Splits
 * `rows` evenly between the two columns so the visual rhythm matches a
 * single ranked list rather than two independent lists.
 *
 * Both the model name and vendor name are clickable: model jumps to
 * `/pricing/{modelName}` and vendor jumps to `/pricing?vendor={vendor}`.
 */
export function ModelLeaderboard(props: ModelLeaderboardProps) {
  const limited = props.limit ? props.rows.slice(0, props.limit) : props.rows
  const half = Math.ceil(limited.length / 2)
  const left = limited.slice(0, half)
  const right = limited.slice(half)
  const variant = props.variant ?? 'default'

  if (limited.length === 0) {
    return null
  }

  return (
    <div className='grid grid-cols-1 gap-x-8 md:grid-cols-2'>
      <ModelList rows={left} variant={variant} />
      {right.length > 0 && <ModelList rows={right} variant={variant} />}
    </div>
  )
}

function ModelList(props: {
  rows: ModelRanking[]
  variant: 'default' | 'compact'
}) {
  const { t } = useTranslation()
  const compact = props.variant === 'compact'
  return (
    <ul>
      {props.rows.map((row) => (
        <li
          key={row.model_name}
          className={
            compact
              ? 'flex items-center gap-3 py-2'
              : 'flex items-center gap-3 py-2.5'
          }
        >
          <span className='text-muted-foreground/80 w-6 shrink-0 text-right font-mono text-xs tabular-nums'>
            {row.rank}.
          </span>
          <span className='shrink-0'>
            {getLobeIcon(row.vendor_icon, compact ? 20 : 22)}
          </span>
          <div className='min-w-0 flex-1'>
            <ModelLink
              modelName={row.model_name}
              className={
                compact
                  ? 'text-foreground block truncate font-mono text-xs font-medium'
                  : 'text-foreground block truncate font-mono text-sm font-medium'
              }
            >
              {row.model_name}
            </ModelLink>
            <p
              className={
                compact
                  ? 'text-muted-foreground/80 truncate text-[11px] italic'
                  : 'text-muted-foreground/80 truncate text-xs italic'
              }
            >
              by{' '}
              <VendorLink vendor={row.vendor}>
                {row.vendor.toLowerCase()}
              </VendorLink>
            </p>
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={
                compact
                  ? 'text-foreground font-mono text-xs font-semibold tabular-nums'
                  : 'text-foreground font-mono text-sm font-semibold tabular-nums'
              }
            >
              {formatTokens(row.total_tokens)}
              {!compact && (
                <>
                  {' '}
                  <span className='text-muted-foreground/80 font-normal'>
                    {t('tokens')}
                  </span>
                </>
              )}
            </div>
            <GrowthText
              value={row.growth_pct}
              className={compact ? 'text-[10px]' : 'text-[11px]'}
            />
          </div>
        </li>
      ))}
    </ul>
  )
}
