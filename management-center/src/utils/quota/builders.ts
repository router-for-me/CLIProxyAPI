/**
 * Builder functions for constructing quota data structures.
 */

import type {
  AntigravityQuotaBucket,
  AntigravityQuotaGroup,
  AntigravityQuotaSummaryPayload,
  KimiUsagePayload,
  KimiUsageDetail,
  KimiLimitItem,
  KimiLimitWindow,
  KimiQuotaRow,
  XaiBillingConfig,
  XaiBillingPeriod,
  XaiBillingPeriodType,
  XaiBillingSummary,
  XaiProductUsageSummary,
} from '@/types';
import { normalizeNumberValue, normalizeQuotaFraction, normalizeStringValue } from './parsers';
import { parseOffsetSecondsToMs, resolveResetMs } from './resetInstants';

const ANTIGRAVITY_BUCKET_WINDOW_ORDER = new Map<string, number>([
  ['5h', 0],
  ['five-hour', 0],
  ['five_hour', 0],
  ['weekly', 1],
  ['week', 1],
]);

function toStableId(value: string, fallback: string): string {
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
  return normalized || fallback;
}

function getAntigravityWindowOrder(bucket: AntigravityQuotaBucket): number {
  const window = bucket.window?.toLowerCase();
  if (!window) return Number.MAX_SAFE_INTEGER;
  return ANTIGRAVITY_BUCKET_WINDOW_ORDER.get(window) ?? Number.MAX_SAFE_INTEGER;
}

/**
 * Window length in hours for an Antigravity bucket.
 *
 * Antigravity states the period explicitly in `window`, so unlike Kimi this is
 * a lookup rather than a keyword guess. The accepted spellings mirror
 * ANTIGRAVITY_BUCKET_WINDOW_ORDER above.
 */
function antigravityPeriodHours(window: string | undefined): number | null {
  switch ((window ?? '').trim().toLowerCase()) {
    case '5h':
    case 'five-hour':
    case 'five_hour':
      return 5;
    case 'weekly':
    case 'week':
      return 24 * 7;
    default:
      return null;
  }
}

export function buildAntigravityQuotaGroups(
  payload: AntigravityQuotaSummaryPayload
): AntigravityQuotaGroup[] {
  const groups = Array.isArray(payload.groups) ? payload.groups : [];

  return groups
    .map((group, groupIndex): AntigravityQuotaGroup | null => {
      const label =
        normalizeStringValue(group.displayName ?? group.display_name) ??
        `Quota Group ${groupIndex + 1}`;
      const groupId = toStableId(label, `quota-group-${groupIndex + 1}`);
      const buckets = Array.isArray(group.buckets) ? group.buckets : [];
      const parsedBuckets = buckets
        .map((bucket, bucketIndex): AntigravityQuotaBucket | null => {
          const remainingFraction = normalizeQuotaFraction(
            bucket.remainingFraction ?? bucket.remaining_fraction
          );
          if (remainingFraction === null) return null;

          const window = normalizeStringValue(bucket.window) ?? undefined;
          const rawId =
            normalizeStringValue(bucket.bucketId ?? bucket.bucket_id) ??
            `${groupId}-${window ?? `bucket-${bucketIndex + 1}`}`;
          const label = normalizeStringValue(bucket.displayName ?? bucket.display_name) ?? rawId;

          const resetTime =
            normalizeStringValue(bucket.resetTime ?? bucket.reset_time) ?? undefined;

          return {
            id: rawId,
            label,
            window,
            remainingFraction,
            resetTime,
            description: normalizeStringValue(bucket.description) ?? undefined,
            resetAtMs: resolveResetMs([resetTime]),
            periodHours: antigravityPeriodHours(window),
          };
        })
        .filter((bucket): bucket is AntigravityQuotaBucket => bucket !== null)
        .sort((a, b) => {
          const orderDiff = getAntigravityWindowOrder(a) - getAntigravityWindowOrder(b);
          if (orderDiff !== 0) return orderDiff;
          return a.label.localeCompare(b.label);
        });

      if (parsedBuckets.length === 0) return null;

      return {
        id: groupId,
        label,
        description: normalizeStringValue(group.description) ?? undefined,
        buckets: parsedBuckets,
      };
    })
    .filter((group): group is AntigravityQuotaGroup => group !== null);
}

function toInt(value: unknown): number | null {
  if (typeof value === 'number' && Number.isFinite(value)) return Math.floor(value);
  if (typeof value === 'string') {
    const parsed = Number(value.trim());
    return Number.isFinite(parsed) ? Math.floor(parsed) : null;
  }
  return null;
}

type KimiRowLabel = Pick<KimiQuotaRow, 'label' | 'labelKey' | 'labelParams'>;

function formatKimiResetDuration(totalMinutes: number): string {
  const days = Math.floor(totalMinutes / 1440);
  const hours = Math.floor((totalMinutes % 1440) / 60);
  const minutes = totalMinutes % 60;

  if (days > 0) return `${days}d ${hours}h`;
  if (hours > 0 && minutes > 0) return `${hours}h ${minutes}m`;
  if (hours > 0) return `${hours}h`;
  if (minutes > 0) return `${minutes}m`;
  return '<1m';
}

function kimiResetHint(data: Record<string, unknown>): string | undefined {
  const absoluteKeys = ['reset_at', 'resetAt', 'reset_time', 'resetTime'];
  for (const key of absoluteKeys) {
    const raw = data[key];
    if (typeof raw === 'string' && raw.trim()) {
      try {
        const truncated = raw.replace(/(\.\d{6})\d+/, '$1');
        const date = new Date(truncated);
        if (Number.isNaN(date.getTime())) continue;
        const now = Date.now();
        const delta = date.getTime() - now;
        if (delta <= 0) return undefined;
        const totalMinutes = Math.floor(delta / 60000);
        return formatKimiResetDuration(totalMinutes);
      } catch {
        continue;
      }
    }
  }

  const relativeKeys = ['reset_in', 'resetIn', 'ttl'];
  for (const key of relativeKeys) {
    const raw = toInt(data[key]);
    if (raw !== null && raw > 0) {
      const totalMinutes = Math.floor(raw / 60);
      return formatKimiResetDuration(totalMinutes);
    }
  }

  return undefined;
}

type KimiTimeUnit = 'second' | 'minute' | 'hour' | 'day' | 'week';

/** Kimi currently sends protobuf-style values such as TIME_UNIT_MINUTE. */
function normalizeKimiTimeUnit(rawTimeUnit: unknown): KimiTimeUnit | null {
  const unit =
    typeof rawTimeUnit === 'string'
      ? rawTimeUnit
          .trim()
          .toUpperCase()
          .replace(/^TIME_UNIT_/, '')
      : '';
  if (unit === 'SECONDS' || unit === 'SECOND') return 'second';
  if (!unit || unit === 'MINUTES' || unit === 'MINUTE') return 'minute';
  if (unit === 'HOURS' || unit === 'HOUR') return 'hour';
  if (unit === 'DAYS' || unit === 'DAY') return 'day';
  if (unit === 'WEEKS' || unit === 'WEEK') return 'week';
  return null;
}

function kimiDurationToken(duration: number, rawTimeUnit: unknown): string {
  const unit = normalizeKimiTimeUnit(rawTimeUnit);
  if (unit === 'second') return `${duration}s`;
  if (unit === 'hour') return `${duration}h`;
  if (unit === 'day') return `${duration}d`;
  if (unit === 'week') return `${duration}w`;
  return duration % 60 === 0 ? `${duration / 60}h` : `${duration}m`;
}

function kimiLimitLabel(
  item: KimiLimitItem,
  detail: KimiUsageDetail | KimiLimitItem,
  window: KimiLimitWindow,
  index: number
): KimiRowLabel {
  for (const key of ['name', 'title', 'scope'] as const) {
    const val = (item as Record<string, unknown>)[key] ?? (detail as Record<string, unknown>)[key];
    if (typeof val === 'string' && val.trim()) return { label: val.trim() };
  }

  const duration =
    toInt(window.duration) ??
    toInt((item as Record<string, unknown>).duration) ??
    toInt((detail as Record<string, unknown>).duration);
  const timeUnit =
    (window as Record<string, unknown>).timeUnit ??
    (item as Record<string, unknown>).timeUnit ??
    (detail as Record<string, unknown>).timeUnit;

  if (duration !== null && duration > 0) {
    return {
      labelKey: 'kimi_quota.limit_window',
      labelParams: {
        duration: kimiDurationToken(duration, timeUnit),
      },
    };
  }

  return {
    labelKey: 'kimi_quota.limit_index',
    labelParams: {
      index: index + 1,
    },
  };
}

/**
 * Absolute reset instant for a Kimi row.
 *
 * Kimi sends either an absolute timestamp or a relative countdown; the display
 * hint collapses both to something like "3h 20m", which can't be positioned on
 * a timeline. This keeps the instant.
 */
function kimiResetMs(data: Record<string, unknown>): number | null {
  const absolute = resolveResetMs([data.reset_at, data.resetAt, data.reset_time, data.resetTime]);
  if (absolute !== null) return absolute;

  const now = Date.now();
  for (const key of ['reset_in', 'resetIn', 'ttl']) {
    const relative = parseOffsetSecondsToMs(data[key], now);
    if (relative !== null) return relative;
  }
  return null;
}

/** Window length in hours from explicit duration metadata, then label/scope. */
function kimiPeriodHours(
  label: string | undefined,
  duration: number | null = null,
  rawTimeUnit?: unknown
): number | null {
  if (duration !== null && duration > 0) {
    const unit = normalizeKimiTimeUnit(rawTimeUnit);
    if (unit === 'second') return duration / 3600;
    if (unit === 'hour') return duration;
    if (unit === 'day') return duration * 24;
    if (unit === 'week') return duration * 7 * 24;
    // Match the card-label fallback: an absent or unknown unit is treated as minutes.
    return duration / 60;
  }

  const text = (label ?? '').toLowerCase();
  if (text.includes('daily') || text.includes('day')) return 24;
  if (text.includes('weekly') || text.includes('week')) return 24 * 7;
  if (text.includes('monthly') || text.includes('month')) return 24 * 30;
  if (text.includes('5h') || text.includes('hour')) return 5;
  return null;
}

function toKimiUsageRow(
  data: Record<string, unknown>,
  fallbackLabel: KimiRowLabel,
  duration: number | null = null,
  timeUnit?: unknown
):
  | (KimiRowLabel & {
      used: number;
      limit: number;
      resetHint?: string;
      resetAtMs?: number | null;
      periodHours?: number | null;
    })
  | null {
  const limit = toInt(data.limit);
  let used = toInt(data.used);
  if (used === null) {
    const remaining = toInt(data.remaining);
    if (remaining !== null && limit !== null) {
      used = limit - remaining;
    }
  }
  if (used === null && limit === null) return null;
  const explicitLabel =
    (typeof data.name === 'string' && data.name.trim()) ||
    (typeof data.title === 'string' && data.title.trim());
  const label = explicitLabel ? { label: explicitLabel } : fallbackLabel;
  return {
    ...label,
    used: used ?? 0,
    limit: limit ?? 0,
    resetHint: kimiResetHint(data),
    resetAtMs: kimiResetMs(data),
    periodHours: kimiPeriodHours(
      explicitLabel || fallbackLabel.label || fallbackLabel.labelKey,
      duration,
      timeUnit
    ),
  };
}

export function buildKimiQuotaRows(payload: KimiUsagePayload): KimiQuotaRow[] {
  const rows: KimiQuotaRow[] = [];

  const limits = payload.limits;
  if (Array.isArray(limits)) {
    limits.forEach((item, idx) => {
      const detail = (item.detail && typeof item.detail === 'object' ? item.detail : item) as
        KimiUsageDetail | KimiLimitItem;
      const window = (
        item.window && typeof item.window === 'object' ? item.window : {}
      ) as KimiLimitWindow;
      const fallbackLabel = kimiLimitLabel(item, detail, window, idx);
      const duration =
        toInt(window.duration) ??
        toInt((item as Record<string, unknown>).duration) ??
        toInt((detail as Record<string, unknown>).duration);
      const timeUnit =
        (window as Record<string, unknown>).timeUnit ??
        (item as Record<string, unknown>).timeUnit ??
        (detail as Record<string, unknown>).timeUnit;
      const row = toKimiUsageRow(
        detail as Record<string, unknown>,
        fallbackLabel,
        duration,
        timeUnit
      );
      if (row) {
        rows.push({ id: `limit-${idx}`, ...row });
      }
    });
  }

  const usage = payload.usage;
  if (usage && typeof usage === 'object') {
    const summary = toKimiUsageRow(usage as Record<string, unknown>, {
      labelKey: 'kimi_quota.weekly_limit',
    });
    if (summary) {
      rows.push({ id: 'summary', ...summary });
    }
  }

  return rows;
}

function normalizeXaiCentValue(value: XaiBillingConfig['monthlyLimit']): number | null {
  if (value === undefined || value === null) return null;
  if (typeof value === 'object' && !Array.isArray(value)) {
    return normalizeNumberValue((value as { val?: unknown }).val);
  }
  return normalizeNumberValue(value);
}

function resolveXaiPeriodType(period?: XaiBillingPeriod | null): XaiBillingPeriodType {
  const rawType = normalizeStringValue(period?.type)?.toLowerCase() ?? '';
  if (rawType.includes('weekly')) return 'weekly';
  if (rawType.includes('monthly')) return 'monthly';
  return 'unknown';
}

function normalizeXaiProductUsage(
  productUsage: XaiBillingConfig['productUsage'],
  fallbackPrefix: string
): XaiProductUsageSummary[] {
  if (!Array.isArray(productUsage)) return [];

  return productUsage
    .map((item, index): XaiProductUsageSummary | null => {
      if (!item || typeof item !== 'object') return null;
      const product = normalizeStringValue(item.product) ?? `${fallbackPrefix} ${index + 1}`;
      const usagePercent = normalizeNumberValue(item.usagePercent ?? item.usage_percent);
      return { product, usagePercent };
    })
    .filter((item): item is XaiProductUsageSummary => item !== null);
}

const emptyXaiBillingSummary = (): XaiBillingSummary => ({
  mode: 'billing',
  source: 'cli-chat-proxy',
  periodType: 'unknown',
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent: null,
});

/**
 * Reset instant and length of an xAI period.
 *
 * The length comes from the period's own start → end span rather than being
 * assumed, so a non-standard cycle still positions correctly. Null when the
 * payload states an end without a start; the caller supplies the default it
 * knows is right for the period type.
 */
function xaiPeriodInstants(
  periodStart: string | undefined,
  periodEnd: string | undefined
): { resetAtMs: number | null; periodHours: number | null } {
  const resetAtMs = resolveResetMs([periodEnd]);
  const startMs = resolveResetMs([periodStart]);
  const periodHours =
    resetAtMs !== null && startMs !== null && resetAtMs > startMs
      ? (resetAtMs - startMs) / 3_600_000
      : null;
  return { resetAtMs, periodHours };
}

export function buildXaiBillingSummary(
  config: XaiBillingConfig | null | undefined
): XaiBillingSummary | null {
  if (!config || typeof config !== 'object') return null;

  const summary = emptyXaiBillingSummary();
  const currentPeriod = config.currentPeriod ?? config.current_period ?? null;
  const periodType = resolveXaiPeriodType(currentPeriod);
  const creditUsagePercent = normalizeNumberValue(
    config.creditUsagePercent ?? config.credit_usage_percent
  );
  const periodStart =
    normalizeStringValue(currentPeriod?.start) ??
    normalizeStringValue(config.billingPeriodStart ?? config.billing_period_start) ??
    undefined;
  const periodEnd =
    normalizeStringValue(currentPeriod?.end) ??
    normalizeStringValue(config.billingPeriodEnd ?? config.billing_period_end) ??
    undefined;
  const productUsage = normalizeXaiProductUsage(
    config.productUsage ?? config.product_usage,
    'Product'
  );

  const monthlyLimitCents = normalizeXaiCentValue(config.monthlyLimit ?? config.monthly_limit);
  const usedCents = normalizeXaiCentValue(config.used);
  const onDemandCapCents = normalizeXaiCentValue(config.onDemandCap ?? config.on_demand_cap);
  const explicitOnDemandUsedCents = normalizeXaiCentValue(
    config.onDemandUsed ?? config.on_demand_used
  );
  const billingPeriodStart =
    normalizeStringValue(config.billingPeriodStart ?? config.billing_period_start) ?? undefined;
  const billingPeriodEnd =
    normalizeStringValue(config.billingPeriodEnd ?? config.billing_period_end) ?? undefined;

  const includedUsedCents =
    usedCents === null
      ? null
      : monthlyLimitCents !== null && monthlyLimitCents > 0
        ? Math.min(usedCents, monthlyLimitCents)
        : usedCents;
  const derivedOnDemandUsedCents =
    usedCents !== null && monthlyLimitCents !== null
      ? Math.max(0, usedCents - monthlyLimitCents)
      : null;
  const onDemandUsedCents = explicitOnDemandUsedCents ?? derivedOnDemandUsedCents;
  const usedPercent =
    monthlyLimitCents !== null && monthlyLimitCents > 0 && includedUsedCents !== null
      ? (includedUsedCents / monthlyLimitCents) * 100
      : null;
  const onDemandUsedPercent =
    onDemandCapCents !== null && onDemandCapCents > 0 && onDemandUsedCents !== null
      ? (onDemandUsedCents / onDemandCapCents) * 100
      : null;

  const hasWeeklyData =
    creditUsagePercent !== null || periodType === 'weekly' || productUsage.length > 0;
  const hasMonthlyData =
    monthlyLimitCents !== null ||
    usedCents !== null ||
    (!hasWeeklyData && (onDemandCapCents !== null || !!billingPeriodEnd));

  if (!hasWeeklyData && !hasMonthlyData) return null;

  summary.periodType = hasWeeklyData
    ? periodType === 'unknown'
      ? 'weekly'
      : periodType
    : 'monthly';
  summary.usagePercent = hasWeeklyData ? creditUsagePercent : usedPercent;
  summary.periodStart = hasWeeklyData ? periodStart : billingPeriodStart;
  summary.periodEnd = hasWeeklyData ? periodEnd : billingPeriodEnd;
  summary.productUsage = productUsage;
  summary.monthlyLimitCents = monthlyLimitCents;
  summary.usedCents = usedCents;
  summary.includedUsedCents = includedUsedCents;
  summary.onDemandCapCents = onDemandCapCents;
  summary.onDemandUsedCents = onDemandUsedCents;
  summary.onDemandUsedPercent = onDemandUsedPercent;
  summary.billingPeriodStart = hasMonthlyData ? billingPeriodStart : undefined;
  summary.billingPeriodEnd = hasMonthlyData ? billingPeriodEnd : undefined;
  summary.usedPercent = usedPercent;

  const periodInstants = xaiPeriodInstants(summary.periodStart, summary.periodEnd);
  summary.resetAtMs = periodInstants.resetAtMs;
  summary.periodHours = periodInstants.periodHours;

  return summary;
}

export function mergeXaiBillingSummaries(
  primary: XaiBillingSummary | null,
  fallback: XaiBillingSummary | null
): XaiBillingSummary | null {
  if (!primary) return fallback;
  if (!fallback) return primary;

  // Keep the active period atomic. The primary (weekly endpoint) and fallback
  // (monthly endpoint) describe different clocks, so borrowing one endpoint's
  // dates for the other's period type would turn a billing rollover into a
  // quota reset. Keep usage with that same period too: monthly spending must
  // not replace an unavailable weekly percentage.
  const periodSummary =
    primary.periodType !== 'unknown'
      ? primary
      : fallback.periodType !== 'unknown'
        ? fallback
        : primary;
  const periodStart = periodSummary.periodStart;
  const periodEnd = periodSummary.periodEnd;
  const periodInstants = xaiPeriodInstants(periodStart, periodEnd);

  return {
    mode: 'billing',
    source: 'cli-chat-proxy',
    periodType: periodSummary.periodType,
    usagePercent: periodSummary.usagePercent,
    periodStart,
    periodEnd,
    resetAtMs: periodInstants.resetAtMs,
    periodHours: periodInstants.periodHours,
    productUsage: primary.productUsage.length > 0 ? primary.productUsage : fallback.productUsage,
    monthlyLimitCents: primary.monthlyLimitCents ?? fallback.monthlyLimitCents,
    usedCents: primary.usedCents ?? fallback.usedCents,
    includedUsedCents: primary.includedUsedCents ?? fallback.includedUsedCents,
    onDemandCapCents: primary.onDemandCapCents ?? fallback.onDemandCapCents,
    onDemandUsedCents: primary.onDemandUsedCents ?? fallback.onDemandUsedCents,
    onDemandUsedPercent: primary.onDemandUsedPercent ?? fallback.onDemandUsedPercent,
    billingPeriodStart: primary.billingPeriodStart ?? fallback.billingPeriodStart,
    billingPeriodEnd: primary.billingPeriodEnd ?? fallback.billingPeriodEnd,
    usedPercent: primary.usedPercent ?? fallback.usedPercent,
  };
}
