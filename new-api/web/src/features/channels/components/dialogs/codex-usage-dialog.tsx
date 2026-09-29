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
import {
  Copy,
  Check,
  RefreshCw,
  ChevronDown,
  ChevronUp,
  RotateCcw,
  AlertTriangle,
} from 'lucide-react'
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
import { type ReactNode, useCallback, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { ConfirmDialog } from '@/components/confirm-dialog'
import { Dialog } from '@/components/dialog'
import { StatusBadge, type StatusBadgeProps } from '@/components/status-badge'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from '@/components/ui/collapsible'
import {
  Empty,
  EmptyDescription,
  EmptyHeader,
  EmptyTitle,
} from '@/components/ui/empty'
import { Progress } from '@/components/ui/progress'
import { ScrollArea } from '@/components/ui/scroll-area'
import { Skeleton } from '@/components/ui/skeleton'
import { useCopyToClipboard } from '@/hooks/use-copy-to-clipboard'
import dayjs from '@/lib/dayjs'
import { formatDateTimeStr, formatTimestampToDate } from '@/lib/format'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'

import {
  getCodexResetCredits,
  resetCodexUsage,
  type CodexResetCreditsResponse,
} from '../../api'

type CodexRateLimitWindow = {
  used_percent?: number
  reset_at?: number
  reset_after_seconds?: number
  limit_window_seconds?: number
}

type CodexRateLimit = {
  plan_type?: string
  allowed?: boolean
  limit_reached?: boolean
  primary_window?: CodexRateLimitWindow
  secondary_window?: CodexRateLimitWindow
}

type CodexAdditionalRateLimit = {
  limit_name?: string
  metered_feature?: string
  rate_limit?: CodexRateLimit
  primary_window?: CodexRateLimitWindow
  secondary_window?: CodexRateLimitWindow
  plan_type?: string
}

type CodexResetCredit = {
  id?: string
  reset_type?: string
  status?: string
  granted_at?: string | null
  expires_at?: string | null
  redeem_started_at?: string | null
  redeemed_at?: string | null
  profile_image_url?: string
  profile_user_id?: string
  title?: string
  description?: string
}

type CodexResetCreditsPayload = {
  credits?: CodexResetCredit[]
  available_count?: number
  total_earned_count?: number
}

type CodexUsagePayload = {
  plan_type?: string
  user_id?: string
  email?: string
  rate_limit?: CodexRateLimit
  additional_rate_limits?: CodexAdditionalRateLimit[]
  rate_limit_reset_credits?: {
    available_count?: number
  }
  credits?: {
    overage_limit_reached?: boolean
  }
  spend_control?: {
    reached?: boolean
  }
}

export type CodexUsageDialogData = {
  success: boolean
  message?: string
  upstream_status?: number
  data?: Record<string, unknown>
}

type CodexUsageDialogProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  channelName?: string
  channelId?: number
  channelDisplayName?: string
  channelDisplayId?: string
  response: CodexUsageDialogData | null
  onRefresh?: () => void | Promise<void>
  isRefreshing?: boolean
}

function clampPercent(value: unknown): number {
  const v = Number(value)
  return Number.isFinite(v) ? Math.max(0, Math.min(100, v)) : 0
}

function formatUnixSeconds(unixSeconds: unknown): string {
  const v = Number(unixSeconds)
  return Number.isFinite(v) && v > 0 ? formatTimestampToDate(v) : '-'
}

function formatIsoTimestamp(value: unknown): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '-'
  }
  const d = dayjs(value)
  if (!d.isValid()) {
    return value
  }
  return formatDateTimeStr(d.toDate())
}

function formatDurationSeconds(
  seconds: unknown,
  t: (key: string) => string
): string {
  const s = Number(seconds)
  if (!Number.isFinite(s) || s <= 0) {
    return '-'
  }

  const total = Math.floor(s)
  const hours = Math.floor(total / 3600)
  const minutes = Math.floor((total % 3600) / 60)
  const secs = total % 60

  if (hours > 0) {
    return `${hours}${t('h')} ${minutes}${t('m')}`
  }
  if (minutes > 0) {
    return `${minutes}${t('m')} ${secs}${t('s')}`
  }
  return `${secs}${t('s')}`
}

function formatTimeLeftUntil(
  value: unknown,
  t: (key: string) => string
): string {
  if (typeof value !== 'string' || value.trim() === '') {
    return '-'
  }
  const expiresAt = dayjs(value)
  if (!expiresAt.isValid()) {
    return '-'
  }

  const secondsLeft = expiresAt.diff(dayjs(), 'second')
  if (secondsLeft <= 0) {
    return t('Expired')
  }

  const days = Math.floor(secondsLeft / (24 * 60 * 60))
  const remainingSeconds = secondsLeft % (24 * 60 * 60)
  if (days > 0) {
    const hours = Math.floor(remainingSeconds / 3600)
    return `${days} ${t('days')} ${hours}${t('h')}`
  }

  return formatDurationSeconds(secondsLeft, t)
}

function normalizePlanType(value: unknown): string {
  if (value == null) {
    return ''
  }
  return String(value).trim().toLowerCase()
}

function parseTimeValue(value: unknown): number {
  if (typeof value !== 'string' || value.trim() === '') {
    return Number.POSITIVE_INFINITY
  }
  const d = dayjs(value)
  return d.isValid() ? d.valueOf() : Number.POSITIVE_INFINITY
}

function normalizeResetCreditStatus(value: unknown): string {
  return String(value || '')
    .trim()
    .toLowerCase()
}

function sortResetCredits(credits: CodexResetCredit[]): CodexResetCredit[] {
  return [...credits].sort((a, b) => {
    const aAvailable = normalizeResetCreditStatus(a.status) === 'available'
    const bAvailable = normalizeResetCreditStatus(b.status) === 'available'
    if (aAvailable !== bAvailable) {
      return aAvailable ? -1 : 1
    }

    const expiresDiff =
      parseTimeValue(a.expires_at) - parseTimeValue(b.expires_at)
    if (expiresDiff !== 0) {
      return expiresDiff
    }

    const grantedDiff =
      parseTimeValue(a.granted_at) - parseTimeValue(b.granted_at)
    if (grantedDiff !== 0) {
      return grantedDiff
    }

    return String(a.id || '').localeCompare(String(b.id || ''))
  })
}

function classifyWindowByDuration(
  windowData?: CodexRateLimitWindow | null
): 'weekly' | 'fiveHour' | null {
  const seconds = Number(windowData?.limit_window_seconds)
  if (!Number.isFinite(seconds) || seconds <= 0) {
    return null
  }
  return seconds >= 24 * 60 * 60 ? 'weekly' : 'fiveHour'
}

type RateLimitSource = {
  plan_type?: string
  rate_limit?: CodexRateLimit
}

function resolveRateLimitWindows(data: RateLimitSource | null): {
  fiveHourWindow: CodexRateLimitWindow | null
  weeklyWindow: CodexRateLimitWindow | null
} {
  const rateLimit = data?.rate_limit ?? {}
  const primary = rateLimit?.primary_window ?? null
  const secondary = rateLimit?.secondary_window ?? null
  const windows = [primary, secondary].filter(Boolean) as CodexRateLimitWindow[]
  const planType = normalizePlanType(data?.plan_type ?? rateLimit?.plan_type)

  let fiveHourWindow: CodexRateLimitWindow | null = null
  let weeklyWindow: CodexRateLimitWindow | null = null

  for (const w of windows) {
    const bucket = classifyWindowByDuration(w)
    if (bucket === 'fiveHour' && !fiveHourWindow) {
      fiveHourWindow = w
      continue
    }
    if (bucket === 'weekly' && !weeklyWindow) {
      weeklyWindow = w
    }
  }

  if (planType === 'free') {
    if (!weeklyWindow) {
      weeklyWindow = primary ?? secondary ?? null
    }
    return { fiveHourWindow: null, weeklyWindow }
  }

  if (!fiveHourWindow && !weeklyWindow) {
    return { fiveHourWindow: primary, weeklyWindow: secondary }
  }

  if (!fiveHourWindow) {
    fiveHourWindow = windows.find((w) => w !== weeklyWindow) ?? null
  }
  if (!weeklyWindow) {
    weeklyWindow = windows.find((w) => w !== fiveHourWindow) ?? null
  }

  return { fiveHourWindow, weeklyWindow }
}

const PLAN_TYPE_BADGE: Record<
  string,
  { label: string; variant: StatusBadgeProps['variant'] }
> = {
  enterprise: { label: 'Enterprise', variant: 'success' },
  team: { label: 'Team', variant: 'info' },
  pro: { label: 'Pro', variant: 'blue' },
  plus: { label: 'Plus', variant: 'purple' },
  free: { label: 'Free', variant: 'warning' },
}

const RESET_CREDIT_STATUS_BADGE: Record<
  string,
  { label: string; variant: StatusBadgeProps['variant'] }
> = {
  available: { label: 'Available', variant: 'success' },
  redeemed: { label: 'Redeemed', variant: 'neutral' },
  expired: { label: 'Expired', variant: 'warning' },
}

function getAccountTypeBadge(
  value: unknown,
  t: (key: string) => string
): { label: string; variant: StatusBadgeProps['variant'] } {
  const normalized = normalizePlanType(value)
  return (
    PLAN_TYPE_BADGE[normalized] ?? {
      label: String(value || '') || t('Unknown'),
      variant: 'neutral' as const,
    }
  )
}

function getResetCreditStatusBadge(
  value: unknown,
  t: (key: string) => string
): { label: string; variant: StatusBadgeProps['variant'] } {
  const normalized = normalizeResetCreditStatus(value)
  return (
    RESET_CREDIT_STATUS_BADGE[normalized] ?? {
      label: String(value || '') || t('Unknown'),
      variant: 'neutral' as const,
    }
  )
}

function windowLabel(windowData?: CodexRateLimitWindow | null) {
  const percent = clampPercent(windowData?.used_percent)
  let variant: StatusBadgeProps['variant'] = 'info'
  if (percent >= 95) {
    variant = 'danger'
  } else if (percent >= 80) {
    variant = 'warning'
  }
  return { percent, variant }
}

function getUsageStatusBadge(
  rateLimit: CodexRateLimit | undefined,
  t: (key: string) => string
) {
  if (!rateLimit || Object.keys(rateLimit).length === 0) {
    return (
      <StatusBadge label={t('Pending')} variant='neutral' copyable={false} />
    )
  }
  if (rateLimit.allowed && !rateLimit.limit_reached) {
    return (
      <StatusBadge label={t('Available')} variant='success' copyable={false} />
    )
  }
  return <StatusBadge label={t('Limited')} variant='danger' copyable={false} />
}

function formatLabelValue(label: string, value: string) {
  return label.endsWith('：') ? `${label}${value}` : `${label} ${value}`
}

const percentTextClassName: Record<
  NonNullable<StatusBadgeProps['variant']>,
  string
> = {
  success: 'text-success',
  warning: 'text-warning',
  danger: 'text-destructive',
  info: 'text-info',
  neutral: 'text-muted-foreground',
  purple: 'text-chart-4',
  amber: 'text-warning',
  blue: 'text-chart-1',
  cyan: 'text-chart-2',
  green: 'text-success',
  grey: 'text-muted-foreground',
  indigo: 'text-chart-1',
  'light-blue': 'text-info',
  'light-green': 'text-emerald-500 dark:text-emerald-300',
  lime: 'text-chart-3',
  orange: 'text-warning',
  pink: 'text-chart-5',
  red: 'text-destructive',
  teal: 'text-chart-2',
  violet: 'text-chart-4',
  yellow: 'text-warning',
}

type RateLimitWindowProps = {
  title: string
  window?: CodexRateLimitWindow | null
}

function RateLimitWindow(props: RateLimitWindowProps) {
  const { t } = useTranslation()
  const hasData =
    !!props.window &&
    typeof props.window === 'object' &&
    Object.keys(props.window).length > 0
  const { percent, variant } = windowLabel(props.window)

  return (
    <Card size='sm' className='gap-0 py-0'>
      <CardHeader className='p-3 pb-2'>
        <div className='flex items-start justify-between gap-3'>
          <div className='min-w-0'>
            <CardTitle className='text-sm font-semibold'>
              {props.title}
            </CardTitle>
            <CardDescription className='mt-1 text-xs'>
              {t('Window:')}{' '}
              {hasData
                ? formatDurationSeconds(props.window?.limit_window_seconds, t)
                : '-'}
            </CardDescription>
          </div>
          <div className='shrink-0 text-right'>
            <div
              className={cn(
                'text-xl leading-none font-semibold tabular-nums',
                percentTextClassName[variant ?? 'neutral']
              )}
            >
              {hasData ? `${percent}%` : '-'}
            </div>
            <div className='text-muted-foreground mt-1 text-[11px]'>
              {t('Used')}
            </div>
          </div>
        </div>
      </CardHeader>
      <CardContent className='p-3 pt-0'>
        {hasData ? (
          <Progress
            value={percent}
            aria-label={`${props.title} usage: ${percent}%`}
            className='mt-1'
          />
        ) : (
          <div className='text-muted-foreground mt-1 text-sm'>-</div>
        )}
        <div className='mt-3 grid grid-cols-1 gap-2 text-xs sm:grid-cols-2'>
          <div className='min-w-0'>
            <div className='text-muted-foreground text-[11px]'>
              {t('Reset at:')}
            </div>
            <div className='break-all tabular-nums'>
              {hasData ? formatUnixSeconds(props.window?.reset_at) : '-'}
            </div>
          </div>
          <div className='min-w-0 sm:text-right'>
            <div className='text-muted-foreground text-[11px]'>
              {t('Resets in:')}
            </div>
            <div className='tabular-nums'>
              {hasData
                ? formatDurationSeconds(props.window?.reset_after_seconds, t)
                : '-'}
            </div>
          </div>
        </div>
      </CardContent>
    </Card>
  )
}

function RateLimitWindowGrid(props: {
  fiveHourWindow?: CodexRateLimitWindow | null
  weeklyWindow?: CodexRateLimitWindow | null
}) {
  const { t } = useTranslation()

  return (
    <div className='grid grid-cols-1 gap-3 md:grid-cols-2'>
      <RateLimitWindow
        title={t('5-Hour Window')}
        window={props.fiveHourWindow}
      />
      <RateLimitWindow title={t('Weekly Window')} window={props.weeklyWindow} />
    </div>
  )
}

function SectionHeading(props: {
  title: string
  description?: string
  children?: ReactNode
}) {
  return (
    <div className='flex flex-wrap items-start justify-between gap-3'>
      <div className='min-w-0'>
        <div className='text-sm font-semibold'>{props.title}</div>
        {props.description ? (
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {props.description}
          </div>
        ) : null}
      </div>
      {props.children ? (
        <div className='flex shrink-0 flex-wrap items-center gap-2'>
          {props.children}
        </div>
      ) : null}
    </div>
  )
}

type RateLimitGroupSectionProps = {
  title: string
  description?: string
  source: RateLimitSource | null
  meteredFeature?: string
}

function RateLimitGroupSection(props: RateLimitGroupSectionProps) {
  const { t } = useTranslation()
  const { fiveHourWindow, weeklyWindow } = resolveRateLimitWindows(props.source)
  const statusBadge = getUsageStatusBadge(props.source?.rate_limit, t)

  return (
    <section className='bg-muted/40 flex flex-col gap-3 rounded-xl p-3'>
      <SectionHeading title={props.title} description={props.description}>
        {statusBadge}
      </SectionHeading>
      {props.meteredFeature ? (
        <div className='bg-background ring-border/60 inline-flex max-w-full flex-wrap items-center gap-x-2 gap-y-1 rounded-lg px-2 py-1 text-xs ring-1'>
          <span className='text-muted-foreground text-[11px]'>
            metered_feature
          </span>
          <span className='min-w-0 font-mono break-all'>
            {props.meteredFeature}
          </span>
        </div>
      ) : null}
      <RateLimitWindowGrid
        fiveHourWindow={fiveHourWindow}
        weeklyWindow={weeklyWindow}
      />
    </section>
  )
}

function InfoField(props: {
  label: string
  value?: string | null
  mono?: boolean
  copyable?: boolean
  className?: string
}) {
  const { t } = useTranslation()
  const { copyToClipboard, copiedText } = useCopyToClipboard({ notify: false })
  const text = props.value?.trim() || ''
  const hasCopied = copiedText === text

  return (
    <div
      className={cn(
        'bg-background ring-border/60 min-w-0 rounded-lg p-3 ring-1',
        props.className
      )}
    >
      <div className='text-muted-foreground text-[11px] font-medium'>
        {props.label}
      </div>
      <div className='mt-1 flex min-w-0 items-start justify-between gap-2'>
        <span
          className={cn(
            'min-w-0 flex-1 text-xs leading-5 break-all',
            props.mono && 'font-mono tabular-nums'
          )}
        >
          {text || '-'}
        </span>
        {props.copyable !== false && text ? (
          <Button
            type='button'
            variant='ghost'
            size='icon-xs'
            aria-label={t('Copy')}
            onClick={() => copyToClipboard(text)}
          >
            {hasCopied ? <Check className='text-success' /> : <Copy />}
          </Button>
        ) : null}
      </div>
    </div>
  )
}

function ResetCreditTimeField(props: {
  label: string
  value: string
  emphasis?: boolean
}) {
  return (
    <div className='min-w-0'>
      <div className='text-muted-foreground text-[11px] font-medium'>
        {props.label}
      </div>
      <div
        className={cn(
          'mt-1 text-xs leading-5 tabular-nums',
          props.emphasis ? 'font-semibold' : 'text-foreground'
        )}
      >
        {props.value}
      </div>
    </div>
  )
}

function ResetCreditItem(props: { credit: CodexResetCredit; index: number }) {
  const { t } = useTranslation()
  const statusBadge = getResetCreditStatusBadge(props.credit.status, t)
  const title =
    props.credit.title?.trim() || `${t('Reset Credit')} ${props.index + 1}`
  const expiresIn = formatTimeLeftUntil(props.credit.expires_at, t)
  const isAvailable =
    normalizeResetCreditStatus(props.credit.status) === 'available'

  return (
    <div className='bg-background rounded-lg border p-3'>
      <div className='flex flex-wrap items-start justify-between gap-3'>
        <div className='min-w-0'>
          <div className='flex flex-wrap items-center gap-2'>
            <div className='min-w-0 text-sm font-medium break-words'>
              {title}
            </div>
            <StatusBadge
              label={t(statusBadge.label)}
              variant={statusBadge.variant}
              copyable={false}
            />
          </div>
          {props.credit.description ? (
            <div className='text-muted-foreground mt-1 text-xs leading-5'>
              {props.credit.description}
            </div>
          ) : null}
          {props.credit.id ? (
            <div className='text-muted-foreground mt-1 font-mono text-[11px] break-all'>
              {props.credit.id}
            </div>
          ) : null}
        </div>
        <div className='shrink-0 text-right'>
          <div className='text-muted-foreground text-[11px] font-medium'>
            {t('Expires in')}
          </div>
          <div
            className={cn(
              'mt-1 text-sm font-semibold tabular-nums',
              isAvailable ? 'text-success' : 'text-muted-foreground'
            )}
          >
            {expiresIn}
          </div>
        </div>
      </div>
      <div className='mt-3 grid grid-cols-1 gap-3 sm:grid-cols-3'>
        <ResetCreditTimeField
          label={t('Granted at')}
          value={formatIsoTimestamp(props.credit.granted_at)}
        />
        <ResetCreditTimeField
          label={t('Expires at')}
          value={formatIsoTimestamp(props.credit.expires_at)}
        />
        <ResetCreditTimeField
          label={t('Redeemed at')}
          value={formatIsoTimestamp(props.credit.redeemed_at)}
          emphasis={Boolean(props.credit.redeemed_at)}
        />
      </div>
    </div>
  )
}

function ResetCreditsPanel(props: {
  payload: CodexResetCreditsPayload | null
  response: CodexResetCreditsResponse | null
  usageAvailableCount: string
  isLoading: boolean
  isResetting: boolean
  errorMessage: string
  resetErrorMessage: string
  resetSuccessMessage: string
  onRefresh: () => void
  onRequestReset: () => void
}) {
  const { t } = useTranslation()
  const credits = useMemo(
    () => sortResetCredits(props.payload?.credits ?? []),
    [props.payload?.credits]
  )
  const detailAvailableCount = props.payload?.available_count
  const availableCount = Number.isFinite(Number(detailAvailableCount))
    ? String(detailAvailableCount)
    : props.usageAvailableCount
  const totalEarnedCount = Number.isFinite(
    Number(props.payload?.total_earned_count)
  )
    ? String(props.payload?.total_earned_count)
    : '-'
  const canReset = Number(availableCount) > 0
  let creditsContent: ReactNode
  if (props.errorMessage) {
    creditsContent = (
      <div className='border-destructive/40 bg-destructive/10 text-destructive rounded-lg border px-3 py-2 text-sm'>
        {props.errorMessage}
      </div>
    )
  } else if (props.isLoading) {
    creditsContent = (
      <div className='flex flex-col gap-2'>
        <Skeleton className='h-24 w-full' />
        <Skeleton className='h-24 w-full' />
      </div>
    )
  } else if (credits.length > 0) {
    creditsContent = (
      <div className='flex flex-col gap-2'>
        {credits.map((credit, index) => (
          <ResetCreditItem
            key={
              credit.id ??
              credit.expires_at ??
              credit.granted_at ??
              credit.title ??
              credit.reset_type ??
              ''
            }
            credit={credit}
            index={index}
          />
        ))}
      </div>
    )
  } else {
    creditsContent = (
      <Empty className='min-h-32 border'>
        <EmptyHeader>
          <EmptyTitle>{t('No reset credits')}</EmptyTitle>
          <EmptyDescription>
            {t('Upstream did not return reset credit details.')}
          </EmptyDescription>
        </EmptyHeader>
      </Empty>
    )
  }

  return (
    <div className='flex flex-col gap-3 p-3'>
      <div className='grid grid-cols-1 gap-3 sm:grid-cols-3'>
        <InfoField
          label={t('Available reset credits')}
          value={availableCount}
          mono
          copyable={false}
        />
        <InfoField
          label={t('Total earned')}
          value={totalEarnedCount}
          mono
          copyable={false}
        />
        <InfoField
          label='HTTP'
          value={String(props.response?.upstream_status ?? '-')}
          mono
          copyable={false}
        />
      </div>

      <div className='flex flex-wrap items-center justify-between gap-2'>
        <div className='text-muted-foreground text-xs leading-5'>
          {t('Available credits are ordered by soonest expiration.')}
        </div>
        <Button
          type='button'
          variant='outline'
          size='sm'
          onClick={props.onRefresh}
          disabled={props.isLoading}
        >
          <RefreshCw data-icon='inline-start' />
          {t('Refresh details')}
        </Button>
      </div>

      <div className='bg-muted/30 flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center sm:justify-between'>
        <div className='min-w-0'>
          <div className='text-sm font-semibold'>{t('Reset usage window')}</div>
          <div className='text-muted-foreground mt-1 text-xs leading-5'>
            {t(
              'Use one available reset credit to refresh the current Codex usage windows.'
            )}
          </div>
        </div>
        <Button
          type='button'
          variant={canReset ? 'destructive' : 'outline'}
          size='sm'
          onClick={props.onRequestReset}
          disabled={!canReset || props.isLoading || props.isResetting}
          className='shrink-0'
        >
          <RotateCcw data-icon='inline-start' />
          {props.isResetting ? t('Resetting...') : t('Apply reset')}
        </Button>
      </div>

      {!canReset ? (
        <Alert>
          <AlertTriangle />
          <AlertTitle>{t('No reset credits available')}</AlertTitle>
          <AlertDescription>
            {t('The reset request stays disabled until a credit is available.')}
          </AlertDescription>
        </Alert>
      ) : null}

      {props.resetSuccessMessage ? (
        <Alert className='border-success/40 bg-success/10 text-success'>
          <Check />
          <AlertTitle>{t('Reset completed')}</AlertTitle>
          <AlertDescription>{props.resetSuccessMessage}</AlertDescription>
        </Alert>
      ) : null}

      {props.resetErrorMessage ? (
        <Alert variant='destructive'>
          <AlertTriangle />
          <AlertTitle>{t('Reset failed')}</AlertTitle>
          <AlertDescription>{props.resetErrorMessage}</AlertDescription>
        </Alert>
      ) : null}

      {creditsContent}
    </div>
  )
}

export function CodexUsageDialog({
  open,
  onOpenChange,
  channelName,
  channelId,
  channelDisplayName,
  channelDisplayId,
  response,
  onRefresh,
  isRefreshing,
}: CodexUsageDialogProps) {
  const { t } = useTranslation()
  const { copiedText, copyToClipboard } = useCopyToClipboard({ notify: false })
  const [showRawJson, setShowRawJson] = useState(false)
  const [showResetCredits, setShowResetCredits] = useState(false)
  const [resetCreditsResponse, setResetCreditsResponse] =
    useState<CodexResetCreditsResponse | null>(null)
  const [isLoadingResetCredits, setIsLoadingResetCredits] = useState(false)
  const [resetCreditsError, setResetCreditsError] = useState('')
  const [resetConfirmOpen, setResetConfirmOpen] = useState(false)
  const [isResetting, setIsResetting] = useState(false)
  const [resetActionError, setResetActionError] = useState('')
  const [resetActionMessage, setResetActionMessage] = useState('')

  const payload: CodexUsagePayload | null = useMemo(() => {
    const raw = response?.data
    if (!raw || typeof raw !== 'object') {
      return null
    }
    return raw as CodexUsagePayload
  }, [response?.data])

  const resetCreditsPayload: CodexResetCreditsPayload | null = useMemo(() => {
    const raw = resetCreditsResponse?.data
    if (!raw || typeof raw !== 'object') {
      return null
    }
    return raw as CodexResetCreditsPayload
  }, [resetCreditsResponse?.data])

  const rateLimit = payload?.rate_limit
  const accountType = payload?.plan_type ?? rateLimit?.plan_type
  const accountBadge = getAccountTypeBadge(accountType, t)
  const additionalRateLimits = (payload?.additional_rate_limits ?? []).filter(
    (item) => item && Object.keys(item).length > 0
  )
  const resetCredits =
    resetCreditsPayload?.available_count ??
    payload?.rate_limit_reset_credits?.available_count
  const resetCreditsText = Number.isFinite(Number(resetCredits))
    ? String(resetCredits)
    : '-'
  const canResetCodexUsage = Number(resetCredits) > 0
  const channelLabelName = channelDisplayName ?? channelName ?? '-'
  let channelLabelId = ''
  if (channelDisplayId != null) {
    channelLabelId = ` (#${channelDisplayId})`
  } else if (channelId) {
    channelLabelId = ` (#${channelId})`
  }
  const channelLabel = `${channelLabelName}${channelLabelId}`
  const { fiveHourWindow, weeklyWindow } = resolveRateLimitWindows(payload)

  const errorMessage =
    response?.success === false
      ? response?.message?.trim() || t('Failed to fetch usage')
      : ''

  const loadResetCredits = useCallback(
    async (force = false) => {
      if (!channelId) {
        setResetCreditsError(t('Channel ID is required'))
        return
      }
      if (isLoadingResetCredits || (!force && resetCreditsResponse)) {
        return
      }

      setIsLoadingResetCredits(true)
      setResetCreditsError('')
      try {
        const res = await getCodexResetCredits(channelId)
        if (!res.success) {
          throw createServerError(
            res,
            t('Failed to fetch reset credit details')
          )
        }
        setResetCreditsResponse(res)
      } catch (error) {
        setResetCreditsError(
          error instanceof Error
            ? error.message
            : t('Failed to fetch reset credit details')
        )
      } finally {
        setIsLoadingResetCredits(false)
      }
    },
    [channelId, isLoadingResetCredits, resetCreditsResponse, t]
  )

  const handleResetCreditsOpenChange = (nextOpen: boolean) => {
    setShowResetCredits(nextOpen)
    if (nextOpen) {
      void loadResetCredits(false)
    }
  }

  const handleDialogOpenChange = (nextOpen: boolean) => {
    if (!nextOpen) {
      setShowRawJson(false)
      setShowResetCredits(false)
      setResetCreditsResponse(null)
      setResetCreditsError('')
      setIsLoadingResetCredits(false)
      setResetConfirmOpen(false)
      setIsResetting(false)
      setResetActionError('')
      setResetActionMessage('')
    }
    onOpenChange(nextOpen)
  }

  const handleConfirmReset = async () => {
    if (!channelId || isResetting || !canResetCodexUsage) {
      return
    }

    setIsResetting(true)
    setResetActionError('')
    setResetActionMessage('')
    try {
      const res = await resetCodexUsage(channelId)
      if (!res.success) {
        throw createServerError(res, t('Failed to reset usage'))
      }

      const resetPayload = res.data as
        | { windows_reset?: number; code?: string }
        | undefined
      const windowsReset = Number(resetPayload?.windows_reset)
      setResetActionMessage(
        Number.isFinite(windowsReset)
          ? `${t('Reset completed. Latest usage has been refreshed.')} ${t(
              'Affected windows:'
            )} ${windowsReset}`
          : t('Reset completed. Latest usage has been refreshed.')
      )
      setResetConfirmOpen(false)
      await Promise.resolve(onRefresh?.())
      await loadResetCredits(true)
    } catch (error) {
      setResetActionError(
        error instanceof Error ? error.message : t('Failed to reset usage')
      )
    } finally {
      setIsResetting(false)
    }
  }

  const rawJsonText = useMemo(() => {
    if (!response) {
      return ''
    }
    try {
      return JSON.stringify(
        {
          success: response.success,
          message: response.message,
          upstream_status: response.upstream_status,
          data: response.data,
        },
        null,
        2
      )
    } catch {
      return String(response?.data ?? '')
    }
  }, [response])

  return (
    <Dialog
      open={open}
      onOpenChange={handleDialogOpenChange}
      title={t('Codex Account & Usage')}
      contentClassName='sm:max-w-[900px]'
      titleClassName='flex items-center gap-2'
      contentHeight='auto'
      bodyClassName='flex flex-col gap-4'
      footer={
        <Button
          type='button'
          variant='outline'
          onClick={() => handleDialogOpenChange(false)}
        >
          {t('Close')}
        </Button>
      }
    >
      <div className='flex flex-col gap-4'>
        {errorMessage && (
          <div className='rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-800 dark:bg-red-950/30 dark:text-red-400'>
            {errorMessage}
          </div>
        )}

        <Card size='sm' className='bg-muted/30 gap-0 py-0'>
          <CardHeader className='p-4 pb-2'>
            <CardTitle className='text-muted-foreground text-xs font-medium'>
              {t('Codex Account Status')}
            </CardTitle>
            {onRefresh ? (
              <CardAction>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={onRefresh}
                  disabled={Boolean(isRefreshing)}
                >
                  <RefreshCw data-icon='inline-start' />
                  {t('Refresh')}
                </Button>
              </CardAction>
            ) : null}
          </CardHeader>
          <CardContent className='p-4 pt-0'>
            <div className='flex flex-wrap items-center gap-2'>
              <StatusBadge
                label={accountBadge.label}
                variant={accountBadge.variant}
                copyable={false}
              />
              {getUsageStatusBadge(rateLimit, t)}
              <StatusBadge
                label={`HTTP ${response?.upstream_status ?? '-'}`}
                variant='neutral'
                copyable={false}
              />
              <StatusBadge
                label={formatLabelValue(t('Reset count:'), resetCreditsText)}
                variant={Number(resetCredits) > 0 ? 'blue' : 'neutral'}
                copyable={false}
              />
              {payload?.credits?.overage_limit_reached ? (
                <StatusBadge
                  label={t('Overage limited')}
                  variant='danger'
                  copyable={false}
                />
              ) : null}
              {payload?.spend_control?.reached ? (
                <StatusBadge
                  label={t('Spend limited')}
                  variant='danger'
                  copyable={false}
                />
              ) : null}
            </div>
            <div className='mt-4 grid grid-cols-1 gap-3 md:grid-cols-2'>
              <InfoField label={t('Email')} value={payload?.email} copyable />
              <InfoField
                label={t('Channel')}
                value={channelLabel}
                copyable={false}
              />
              <InfoField
                label='User ID'
                value={payload?.user_id}
                mono
                className='md:col-span-2'
              />
            </div>
          </CardContent>
        </Card>

        <Collapsible
          open={showResetCredits}
          onOpenChange={handleResetCreditsOpenChange}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-start justify-between gap-3 p-3 text-left transition-colors'
                aria-expanded={showResetCredits}
              />
            }
          >
            <div className='min-w-0'>
              <div className='flex flex-wrap items-center gap-2'>
                <div className='text-sm font-semibold'>
                  {t('Reset Credits')}
                </div>
                <StatusBadge
                  label={`${t('Available')} ${resetCreditsText}`}
                  variant={Number(resetCredits) > 0 ? 'blue' : 'neutral'}
                  copyable={false}
                />
              </div>
              <div className='text-muted-foreground mt-1 text-xs leading-5'>
                {t('View issued reset credits, grant dates, and expiration.')}
              </div>
            </div>
            {showResetCredits ? (
              <ChevronUp className='text-muted-foreground mt-0.5 shrink-0' />
            ) : (
              <ChevronDown className='text-muted-foreground mt-0.5 shrink-0' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent className='border-t'>
            <ResetCreditsPanel
              payload={resetCreditsPayload}
              response={resetCreditsResponse}
              usageAvailableCount={resetCreditsText}
              isLoading={isLoadingResetCredits}
              isResetting={isResetting}
              errorMessage={resetCreditsError}
              resetErrorMessage={resetActionError}
              resetSuccessMessage={resetActionMessage}
              onRefresh={() => void loadResetCredits(true)}
              onRequestReset={() => {
                setResetActionError('')
                setResetActionMessage('')
                setResetConfirmOpen(true)
              }}
            />
          </CollapsibleContent>
        </Collapsible>

        <div className='flex flex-col gap-3'>
          <SectionHeading
            title={t('Base Limits')}
            description={t('Base rate limit windows for this account.')}
          >
            {getUsageStatusBadge(rateLimit, t)}
          </SectionHeading>
          <RateLimitWindowGrid
            fiveHourWindow={fiveHourWindow}
            weeklyWindow={weeklyWindow}
          />
        </div>

        {additionalRateLimits.length > 0 ? (
          <div className='flex flex-col gap-3'>
            <SectionHeading
              title={t('Additional Limits')}
              description={t(
                'Per-feature metered windows split by model or capability.'
              )}
            />
            <div className='flex flex-col gap-3'>
              {additionalRateLimits.map((item) => {
                const limitName =
                  item.limit_name ||
                  item.metered_feature ||
                  t('Additional Limit')
                return (
                  <RateLimitGroupSection
                    key={`${limitName}-${item.metered_feature ?? ''}-${item.plan_type ?? ''}`}
                    title={limitName}
                    description={t('Additional metered capability')}
                    source={item}
                    meteredFeature={item.metered_feature}
                  />
                )
              })}
            </div>
          </div>
        ) : null}

        <Collapsible
          open={showRawJson}
          onOpenChange={setShowRawJson}
          className='rounded-lg border'
        >
          <CollapsibleTrigger
            render={
              <button
                type='button'
                className='hover:bg-muted/40 flex w-full items-center justify-between gap-2 p-3 transition-colors'
                aria-expanded={showRawJson}
              />
            }
          >
            <div className='text-sm font-medium'>{t('Raw JSON')}</div>
            {showRawJson ? (
              <ChevronUp className='text-muted-foreground h-4 w-4' />
            ) : (
              <ChevronDown className='text-muted-foreground h-4 w-4' />
            )}
          </CollapsibleTrigger>
          <CollapsibleContent>
            <>
              <div className='flex justify-end border-t px-3 py-2'>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => copyToClipboard(rawJsonText)}
                  disabled={!rawJsonText}
                >
                  {copiedText === rawJsonText ? (
                    <Check data-icon='inline-start' className='text-success' />
                  ) : (
                    <Copy data-icon='inline-start' />
                  )}
                  {t('Copy')}
                </Button>
              </div>
              <ScrollArea className='max-h-[50vh]'>
                <pre className='bg-muted/30 m-0 p-3 text-xs break-words whitespace-pre-wrap'>
                  {rawJsonText || '-'}
                </pre>
              </ScrollArea>
            </>
          </CollapsibleContent>
        </Collapsible>
      </div>
      <ConfirmDialog
        open={resetConfirmOpen}
        onOpenChange={setResetConfirmOpen}
        title={t('Confirm usage reset')}
        desc={
          <div className='flex flex-col gap-2'>
            <p>
              {t(
                'Use one available reset credit for this channel. The reset request is sent only after confirmation.'
              )}
            </p>
            <div className='bg-muted/50 rounded-lg border px-3 py-2 text-xs'>
              <div className='font-medium'>{channelLabel}</div>
              <div className='text-muted-foreground mt-1'>
                {t('Available reset credits')}: {resetCreditsText}
              </div>
            </div>
            <p className='text-destructive'>
              {t('Used reset credits cannot be restored.')}
            </p>
          </div>
        }
        destructive
        disabled={!canResetCodexUsage}
        isLoading={isResetting}
        cancelBtnText={t('Cancel')}
        confirmText={isResetting ? t('Resetting...') : t('Apply reset')}
        handleConfirm={handleConfirmReset}
      />
    </Dialog>
  )
}
