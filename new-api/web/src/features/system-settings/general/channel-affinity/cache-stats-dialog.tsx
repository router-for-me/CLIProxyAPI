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
import { useEffect, useMemo, useState, useRef } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { formatTimestampToDate } from '@/lib/format'
import { handleServerError } from '@/lib/handle-server-error'

import { getAffinityUsageCache } from './api'

function formatRate(hit: number, total: number): string {
  if (!total || total <= 0) return '-'
  const r = (hit / total) * 100
  if (!Number.isFinite(r)) return '-'
  return `${r.toFixed(2)}%`
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  target: {
    rule_name: string
    using_group: string
    key_hint: string
    key_fp: string
  } | null
}

export function CacheStatsDialog(props: Props) {
  const { t } = useTranslation()
  const [loading, setLoading] = useState(false)
  const [stats, setStats] = useState<Record<string, unknown> | null>(null)
  const seqRef = useRef(0)

  useEffect(() => {
    if (!props.open || !props.target?.rule_name || !props.target?.key_fp) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setStats(null)
      return
    }

    const seq = ++seqRef.current

    setLoading(true)

    setStats(null)

    void getAffinityUsageCache(props.target)
      .then((res) => {
        if (seq !== seqRef.current) return
        if (res.success) setStats((res.data as Record<string, unknown>) || {})
        else handleServerError(res, t('Request failed'))
      })
      .catch((error) => {
        if (seq !== seqRef.current) return
        handleServerError(error, t('Request failed'))
      })
      .finally(() => {
        if (seq !== seqRef.current) return
        setLoading(false)
      })
  }, [props.open, props.target, t])

  const rows = useMemo(() => {
    if (!stats) return []
    const s = stats
    const data: { key: string; value: string | number }[] = []
    const hit = Number(s.hit || 0)
    const total = Number(s.total || 0)

    if (s.rule_name || props.target?.rule_name) {
      data.push({
        key: t('Rule'),
        value: (s.rule_name || props.target?.rule_name || '') as string,
      })
    }
    if (s.using_group || props.target?.using_group) {
      data.push({
        key: t('Group'),
        value: (s.using_group || props.target?.using_group || '') as string,
      })
    }
    if (props.target?.key_hint) {
      data.push({ key: t('Key Summary'), value: props.target.key_hint })
    }
    if (s.key_fp || props.target?.key_fp) {
      data.push({
        key: t('Key Fingerprint'),
        value: (s.key_fp || props.target?.key_fp || '') as string,
      })
    }
    if (Number(s.window_seconds || 0) > 0) {
      data.push({ key: t('TTL (seconds)'), value: s.window_seconds as number })
    }
    if (total > 0) {
      data.push({
        key: t('Hit Rate'),
        value: `${hit}/${total} (${formatRate(hit, total)})`,
      })
    }
    if (Number(s.last_seen_at || 0) > 0) {
      data.push({
        key: t('Last Seen'),
        value: formatTimestampToDate(s.last_seen_at as number | undefined),
      })
    }

    const promptTokens = Number(s.prompt_tokens || 0)
    const cachedTokens = Number(s.cached_tokens || 0)
    const completionTokens = Number(s.completion_tokens || 0)
    const totalTokens = Number(s.total_tokens || 0)

    if (promptTokens > 0) {
      data.push({ key: 'Prompt tokens', value: promptTokens })
    }
    if (cachedTokens > 0) {
      data.push({ key: 'Cached tokens', value: cachedTokens })
    }
    if (completionTokens > 0) {
      data.push({ key: 'Completion tokens', value: completionTokens })
    }
    if (totalTokens > 0) data.push({ key: 'Total tokens', value: totalTokens })

    return data
  }, [stats, props.target, t])

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Channel Affinity: Upstream Cache Hit')}
      contentClassName='sm:max-w-lg'
      contentHeight='auto'
      bodyClassName='space-y-4'
    >
      <p className='text-muted-foreground text-xs'>
        {t(
          'Hit criteria: If cached tokens exist in usage, it counts as a hit.'
        )}
      </p>
      {loading && (
        <div className='text-muted-foreground py-8 text-center text-sm'>
          {t('Loading...')}
        </div>
      )}
      {!loading && rows.length > 0 && (
        <div className='space-y-2'>
          {rows.map((row) => (
            <div
              key={row.key}
              className='flex justify-between gap-4 border-b pb-1 text-sm'
            >
              <span className='text-muted-foreground'>{row.key}</span>
              <span className='text-right font-medium break-all'>
                {row.value}
              </span>
            </div>
          ))}
        </div>
      )}
      {!loading && !(rows.length > 0) && (
        <div className='text-muted-foreground py-8 text-center text-sm'>
          {t('No data available')}
        </div>
      )}
    </Dialog>
  )
}
