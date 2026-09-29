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
import { useQuery } from '@tanstack/react-query'
import { useId, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { LoadingState } from '@/components/loading-state'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { toIntlLocale } from '@/i18n/languages'
import {
  ADMIN_PERMISSION_ACTIONS,
  ADMIN_PERMISSION_RESOURCES,
  hasPermission,
} from '@/lib/admin-permissions'
import { getServerErrorMessage } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getInferenceStatus } from '../../api'
import {
  metricMean,
  metricValue,
  metricRatio,
  recentMetrics,
  type InferenceStatus,
} from '../../lib/inference-status'

type MetricRow = {
  label: string
  details?: string
  value: number | undefined
  unit?: 'percent' | 'seconds' | 'rate'
}

type InferenceStatusDialogProps = {
  provider: 'vllm' | 'sglang'
  channelId: number
  channelName: string
  onClose: () => void
  onSyncModels: () => void
  onTestChannel: () => void
}

// Mounted only while this channel's status dialog is open; unmount aborts the query.
export function InferenceStatusDialog(props: InferenceStatusDialogProps) {
  const { t, i18n } = useTranslation()
  const isSGLang = props.provider === 'sglang'
  const [autoRefresh, setAutoRefresh] = useState(true)
  const autoRefreshId = useId()
  const previous = useRef<
    | { channelId: number; provider: string; snapshot: InferenceStatus }
    | undefined
  >(undefined)
  const user = useAuthStore((state) => state.auth.user)
  const canOperate = hasPermission(
    user,
    ADMIN_PERMISSION_RESOURCES.CHANNEL,
    ADMIN_PERMISSION_ACTIONS.OPERATE
  )
  const canSync =
    canOperate &&
    hasPermission(
      user,
      ADMIN_PERMISSION_RESOURCES.CHANNEL,
      ADMIN_PERMISSION_ACTIONS.WRITE
    )
  const query = useQuery({
    queryKey: ['channels', props.channelId, props.provider, 'inference-status'],
    queryFn: async ({ signal }) => {
      const snapshot = await getInferenceStatus(
        props.channelId,
        props.provider,
        signal
      )
      signal.throwIfAborted()
      const last =
        previous.current?.channelId === props.channelId &&
        previous.current.provider === props.provider
          ? previous.current.snapshot
          : undefined
      const recent = recentMetrics(snapshot, last)
      previous.current = {
        channelId: props.channelId,
        provider: props.provider,
        snapshot,
      }
      return { snapshot, recent }
    },
    refetchInterval: autoRefresh ? 5000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
    meta: { errorToast: false },
  })
  const snapshot = query.data?.snapshot
  const metrics = snapshot?.metrics ?? []
  const recent = query.data?.recent
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const number = new Intl.NumberFormat(locale, {
    maximumFractionDigits: 2,
  })
  const percent = new Intl.NumberFormat(locale, {
    style: 'percent',
    maximumFractionDigits: 2,
  })
  const seconds = new Intl.NumberFormat(locale, {
    style: 'unit',
    unit: 'second',
    maximumFractionDigits: 2,
  })
  const groups: { title: string; hint?: string; rows: MetricRow[] }[] = [
    {
      title: t('Current load'),
      rows: [
        {
          label: t('Running requests'),
          value: metricValue(
            metrics,
            isSGLang ? 'sglang:num_running_reqs' : 'vllm:num_requests_running'
          ),
        },
        {
          label: t('Waiting requests'),
          value: metricValue(
            metrics,
            isSGLang ? 'sglang:num_queue_reqs' : 'vllm:num_requests_waiting'
          ),
        },
        {
          label: isSGLang
            ? t('Peak token pool usage')
            : t('Peak KV cache usage'),
          value: isSGLang
            ? metricValue(metrics, 'sglang:token_usage', {}, 'max')
            : (metricValue(metrics, 'vllm:kv_cache_usage_perc', {}, 'max') ??
              metricValue(metrics, 'vllm:gpu_cache_usage_perc', {}, 'max')),
          unit: 'percent',
        },
        ...(isSGLang
          ? [
              {
                label: t('Output token rate'),
                value: metricValue(metrics, 'sglang:gen_throughput'),
                unit: 'rate' as const,
              },
            ]
          : [
              {
                label: t('Awake engines'),
                value: metricValue(metrics, 'vllm:engine_sleep_state', {
                  sleep_state: 'awake',
                }),
              },
            ]),
      ],
    },
    {
      title: t('Cumulative usage'),
      hint: t(
        'Counters and averages cover the period since the upstream metrics were initialized or reset.'
      ),
      rows: [
        {
          label: isSGLang ? t('Processed requests') : t('Finished requests'),
          value: metricValue(
            metrics,
            isSGLang
              ? 'sglang:num_requests_total'
              : 'vllm:request_success_total'
          ),
        },
        {
          label: t('Input tokens'),
          value: metricValue(metrics, `${props.provider}:prompt_tokens_total`),
        },
        {
          label: t('Output tokens'),
          value: metricValue(
            metrics,
            `${props.provider}:generation_tokens_total`
          ),
        },
        ...(isSGLang
          ? [
              {
                label: t('Cached tokens'),
                value: metricValue(metrics, 'sglang:cached_tokens_total'),
              },
            ]
          : [
              {
                label: t('Prefix cache hit rate'),
                value: metricRatio(
                  metricValue(metrics, 'vllm:prefix_cache_hits_total'),
                  metricValue(metrics, 'vllm:prefix_cache_queries_total')
                ),
                unit: 'percent' as const,
              },
              {
                label: t('Speculative decoding acceptance rate'),
                value: metricRatio(
                  metricValue(
                    metrics,
                    'vllm:spec_decode_num_accepted_tokens_total'
                  ),
                  metricValue(
                    metrics,
                    'vllm:spec_decode_num_draft_tokens_total'
                  )
                ),
                unit: 'percent' as const,
              },
            ]),
        {
          label: t('Mean time to first token'),
          value: metricMean(
            metrics,
            `${props.provider}:time_to_first_token_seconds`
          ),
          unit: 'seconds',
        },
        {
          label: t('Mean request latency'),
          value: metricMean(
            metrics,
            `${props.provider}:e2e_request_latency_seconds`
          ),
          unit: 'seconds',
        },
        {
          label: t('Mean queue time'),
          value: metricMean(
            metrics,
            isSGLang
              ? 'sglang:queue_time_seconds'
              : 'vllm:request_queue_time_seconds'
          ),
          unit: 'seconds',
        },
        {
          label: t('Mean time per output token'),
          value: isSGLang
            ? (metricMean(metrics, 'sglang:inter_token_latency_seconds') ??
              metricMean(metrics, 'sglang:time_per_output_token_seconds'))
            : metricMean(metrics, 'vllm:request_time_per_output_token_seconds'),
          unit: 'seconds',
        },
      ],
    },
    {
      title: t('Since previous sample'),
      hint: recent
        ? t('Sample interval: {{seconds}} seconds', {
            seconds: number.format(recent.seconds),
          })
        : t(
            'Waiting for two comparable samples. Counter resets restart the sampling window.'
          ),
      rows: [
        {
          label: t('Input token rate'),
          value: metricRatio(
            metricValue(
              recent?.metrics ?? [],
              `${props.provider}:prompt_tokens_total`
            ),
            recent?.seconds
          ),
          unit: 'rate',
        },
        {
          label: t('Output token rate'),
          value: metricRatio(
            metricValue(
              recent?.metrics ?? [],
              `${props.provider}:generation_tokens_total`
            ),
            recent?.seconds
          ),
          unit: 'rate',
        },
        {
          label: t('Mean time to first token'),
          value: metricMean(
            recent?.metrics ?? [],
            `${props.provider}:time_to_first_token_seconds`
          ),
          unit: 'seconds',
        },
        {
          label: t('Mean request latency'),
          value: metricMean(
            recent?.metrics ?? [],
            `${props.provider}:e2e_request_latency_seconds`
          ),
          unit: 'seconds',
        },
      ],
    },
  ]

  if (isSGLang) {
    // These SGLang values are per-worker gauges, not cumulative counters.
    // Keep each labelled series rather than adding or averaging percentages.
    const workerMetrics = metrics.filter((metric) =>
      ['sglang:cache_hit_rate', 'sglang:spec_accept_rate'].includes(metric.name)
    )
    if (workerMetrics.length) {
      groups.push({
        title: t('Worker metrics'),
        rows: workerMetrics.map((metric) => ({
          label:
            metric.name === 'sglang:cache_hit_rate'
              ? t('Prefix cache hit rate')
              : t('Speculative decoding acceptance rate'),
          details: JSON.stringify(metric.labels),
          value: metric.value,
          unit: 'percent',
        })),
      })
    }
  }

  const handleExport = () => {
    if (!snapshot) return
    const blob = new Blob([JSON.stringify(snapshot, null, 2)], {
      type: 'application/json',
    })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `${props.provider}-${props.channelId}-${snapshot.sampled_at}.json`
    link.click()
    URL.revokeObjectURL(url)
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) props.onClose()
      }}
      title={isSGLang ? t('SGLang status') : t('vLLM status')}
      description={props.channelName}
      contentClassName='sm:max-w-3xl'
      bodyClassName='space-y-5'
      footer={
        <Button variant='outline' onClick={props.onClose}>
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-wrap items-center gap-2'>
        <Button
          variant='outline'
          disabled={query.isFetching}
          onClick={() => void query.refetch()}
        >
          {t('Refresh')}
        </Button>
        <Button
          variant='outline'
          disabled={!canSync}
          onClick={props.onSyncModels}
        >
          {t('Sync models')}
        </Button>
        <Button
          variant='outline'
          disabled={!canOperate}
          onClick={props.onTestChannel}
        >
          {t('Test Connection')}
        </Button>
        <Button variant='outline' disabled={!snapshot} onClick={handleExport}>
          {t('Export snapshot')}
        </Button>
        <div className='flex items-center gap-2'>
          <Switch
            id={autoRefreshId}
            checked={autoRefresh}
            onCheckedChange={setAutoRefresh}
          />
          <Label htmlFor={autoRefreshId}>{t('Auto refresh (5s)')}</Label>
        </div>
      </div>
      {isSGLang && snapshot?.endpoints['/metrics']?.error && (
        <Alert>
          <AlertDescription>
            {t(
              'Start SGLang with --enable-metrics to expose Prometheus metrics.'
            )}
          </AlertDescription>
        </Alert>
      )}
      {query.isPending && <LoadingState />}
      {query.isError && !snapshot && (
        <ErrorState
          title={
            isSGLang
              ? t('Failed to load SGLang status')
              : t('Failed to load vLLM status')
          }
          description={getServerErrorMessage(query.error)}
          onRetry={() => void query.refetch()}
        />
      )}
      {query.isError && snapshot && (
        <Alert variant='destructive'>
          <AlertDescription>
            {t('Refresh failed. Showing the last successful snapshot.')}
          </AlertDescription>
        </Alert>
      )}
      {snapshot && (
        <>
          <div className='flex flex-wrap gap-x-6 gap-y-2 text-sm'>
            <span>
              {t('Version')}:{' '}
              <strong className='break-all'>
                {snapshot.version || t('Unavailable')}
              </strong>
            </span>
            <span>
              {t('Last updated')}:{' '}
              {new Date(snapshot.sampled_at).toLocaleString(locale)}
            </span>
          </div>
          <dl className='grid grid-cols-2 gap-2 text-sm sm:grid-cols-4'>
            {Object.entries(snapshot.endpoints).map(([path, endpoint]) => (
              <div key={path} className='rounded-md border p-2'>
                <dt className='font-mono text-xs'>{path}</dt>
                <dd
                  className={
                    endpoint.error ? 'text-destructive' : 'text-foreground'
                  }
                >
                  {endpoint.error ? t('Unavailable') : t('Available')}
                  {endpoint.status > 0 && ` · HTTP ${endpoint.status}`}
                  {endpoint.error === 'invalid_response' &&
                    ` · ${t('Invalid response')}`}
                  {endpoint.error === 'response_too_large' &&
                    ` · ${t('Response too large')}`}
                </dd>
              </div>
            ))}
          </dl>
          <section className='space-y-2' aria-label={t('Models')}>
            <h3 className='font-medium'>{t('Models')}</h3>
            {snapshot.models.length === 0 && (
              <p className='text-muted-foreground text-sm'>
                {snapshot.endpoints['/v1/models']?.error
                  ? t('Unavailable')
                  : t('No models found')}
              </p>
            )}
            {snapshot.models.map((model) => (
              <div
                key={model.id}
                className='space-y-1 rounded-md border p-3 text-sm'
              >
                <p className='font-medium break-all'>{model.id}</p>
                <p className='text-muted-foreground break-all'>
                  {t('Model source')}: {model.root || t('Unavailable')}
                </p>
                <p>
                  {t('Max context tokens')}:{' '}
                  {model.max_model_len == null
                    ? t('Unavailable')
                    : number.format(model.max_model_len)}
                </p>
              </div>
            ))}
          </section>
          {groups.map((group) => (
            <section
              key={group.title}
              className='space-y-2'
              aria-label={group.title}
            >
              <h3 className='font-medium'>{group.title}</h3>
              {group.hint && (
                <p className='text-muted-foreground text-xs'>{group.hint}</p>
              )}
              <dl className='grid gap-2 sm:grid-cols-2'>
                {group.rows.map((row) => {
                  let value = t('Unavailable')
                  if (row.value !== undefined) {
                    value = number.format(row.value)
                    if (row.unit === 'percent') {
                      value = percent.format(row.value)
                    }
                    if (row.unit === 'seconds') {
                      value = seconds.format(row.value)
                    }
                    if (row.unit === 'rate') {
                      value = t('{{value}} tokens/s', {
                        value: number.format(row.value),
                      })
                    }
                  }
                  return (
                    <div
                      key={`${row.label}:${row.details ?? ''}`}
                      className='grid min-w-0 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-1 rounded-md border p-3 text-sm'
                    >
                      <dt className='text-muted-foreground break-words'>
                        {row.label}
                      </dt>
                      <dd className='font-medium whitespace-nowrap tabular-nums'>
                        {value}
                      </dd>
                      {row.details && (
                        <dd className='text-muted-foreground col-span-2 break-all text-xs'>
                          {row.details}
                        </dd>
                      )}
                    </div>
                  )
                })}
              </dl>
            </section>
          ))}
        </>
      )}
    </Dialog>
  )
}
