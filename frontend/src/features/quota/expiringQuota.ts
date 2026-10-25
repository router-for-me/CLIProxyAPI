/**
 * Weekly/monthly subscription quota windows that are about to expire.
 *
 * When a period window resets, its unused remainder is lost — this collector
 * surfaces exactly those rows so the page can rank credentials by "use it or
 * lose it". Short cycles (5-hour, daily) are excluded on purpose: they recycle
 * constantly and are not something to plan around.
 *
 * Pure and React-free like resetSchedule.ts: `nowMs` is injected and quota
 * states are read structurally, so every provider shape stays directly
 * testable.
 */

import { DAY_MS } from '@/utils/time/durations';
import { parseIsoToMs } from '@/utils/quota';
import type { QuotaFileEntry } from './logic';
import type { QuotaCardState } from './providers';
import type { QuotaProviderType } from './providers/types';

/** 168h — the shortest period that counts as a weekly/monthly quota. */
export const WEEKLY_PERIOD_HOURS = 24 * 7;

export interface ExpiringQuotaRow {
  /** Unique within one credential's row list. */
  id: string;
  /** Row name shown verbatim (already localized at fetch time). */
  label?: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  /** Optional leading scope, e.g. an Antigravity group or an xAI plan name. */
  prefix?: string;
  prefixKey?: string;
  /** Remaining quota as 0–100; null when the provider reports no figure. */
  remainingPercent: number | null;
  /** Expiry/reset instant in epoch ms. */
  resetAtMs: number;
}

export interface ExpiringQuotaEntry {
  entry: QuotaFileEntry;
  rows: ExpiringQuotaRow[];
  /** Soonest qualifying row instant — the credential sort key. */
  soonestAtMs: number;
}

export type ExpiryUrgency = 'critical' | 'warning' | 'calm';

/** Critical under a day, warning under three — the urgency the card conveys. */
export function expiryUrgency(atMs: number, nowMs: number): ExpiryUrgency {
  const delta = atMs - nowMs;
  if (delta <= DAY_MS) return 'critical';
  if (delta <= 3 * DAY_MS) return 'warning';
  return 'calm';
}

const isFiniteNumber = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value);

const isSuccessState = (quota: unknown): boolean =>
  (quota as { status?: string } | undefined)?.status === 'success';

/** usedPercent (0–100 consumed) → remaining (0–100), clamped. */
const remainingFromUsed = (usedPercent: unknown): number | null =>
  isFiniteNumber(usedPercent) ? Math.min(100, Math.max(0, 100 - usedPercent)) : null;

/**
 * Weekly-or-longer window check. `periodHours` is authoritative when known;
 * when unknown, a reset more than a day out can only belong to a multi-day
 * window, so the distance itself answers the question.
 */
const isLongWindow = (
  periodHours: number | null | undefined,
  resetAtMs: number,
  nowMs: number
): boolean =>
  isFiniteNumber(periodHours) ? periodHours >= WEEKLY_PERIOD_HOURS : resetAtMs - nowMs > DAY_MS;

/** Period bucket for generic labels; null-period rows classify by distance. */
const periodLabelKey = (
  periodHours: number | null | undefined,
  resetAtMs: number,
  nowMs: number
): string => {
  const hours = isFiniteNumber(periodHours) ? periodHours : (resetAtMs - nowMs) / 3_600_000;
  return hours >= 24 * 28
    ? 'sharing.quota_expiry_period_monthly'
    : 'sharing.quota_expiry_period_weekly';
};

/* Structural row shapes — provider states disagree about where a window
   lives, so each is read on its own terms. */

interface WindowShape {
  id?: string;
  label?: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  usedPercent?: number | null;
  remainingPercent?: number | null;
  remainingFraction?: number | null;
  resetAtMs?: number | null;
  periodHours?: number | null;
}

interface KimiRowShape {
  id?: string;
  label?: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  used?: number;
  limit?: number;
  resetAtMs?: number | null;
  periodHours?: number | null;
}

/* Antigravity bucket/group names are upstream English; known ones map onto the
   provider's existing i18n keys, unknown ones render verbatim. */
const ANTIGRAVITY_GROUP_LABEL_KEYS = new Map<string, string>([
  ['gemini models', 'antigravity_quota.group_gemini_models'],
  ['claude and gpt models', 'antigravity_quota.group_claude_gpt_models'],
]);

const ANTIGRAVITY_BUCKET_LABEL_KEYS = new Map<string, string>([
  ['weekly limit', 'antigravity_quota.weekly_limit'],
  ['monthly limit', 'antigravity_quota.monthly_limit'],
]);

const normalizeLabelKey = (value: string): string =>
  value.trim().toLowerCase().replace(/\s+/g, ' ');

export function collectExpiringQuotaRows(
  provider: QuotaProviderType,
  quota: unknown,
  nowMs: number
): ExpiringQuotaRow[] {
  if (!isSuccessState(quota)) return [];

  const rows: ExpiringQuotaRow[] = [];
  const push = (row: {
    id: string;
    label?: string;
    labelKey?: string;
    labelParams?: Record<string, string | number>;
    prefix?: string;
    prefixKey?: string;
    remainingPercent: number | null;
    resetAtMs: number | null | undefined;
    periodHours?: number | null;
  }) => {
    const { resetAtMs, periodHours, remainingPercent } = row;
    if (!isFiniteNumber(resetAtMs) || resetAtMs <= nowMs) return;
    if (remainingPercent !== null && remainingPercent <= 0) return;
    if (!isLongWindow(periodHours, resetAtMs, nowMs)) return;
    rows.push({
      id: row.id,
      label: row.label,
      labelKey: row.labelKey,
      labelParams: row.labelParams,
      prefix: row.prefix,
      prefixKey: row.prefixKey,
      remainingPercent,
      resetAtMs,
    });
  };

  if (provider === 'claude' || provider === 'codex' || provider === 'zcode') {
    const windows = (quota as { windows?: WindowShape[] }).windows ?? [];
    windows.forEach((window, index) =>
      push({
        id: window.id || `window-${index}`,
        label: window.label,
        labelKey: window.labelKey,
        labelParams: window.labelParams,
        remainingPercent: remainingFromUsed(window.usedPercent),
        resetAtMs: window.resetAtMs,
        periodHours: window.periodHours,
      })
    );
  } else if (provider === 'antigravity') {
    const groups =
      (quota as { groups?: { id?: string; label?: string; buckets?: WindowShape[] }[] }).groups ??
      [];
    for (const group of groups) {
      const groupLabel = (group.label ?? '').trim();
      const prefixKey = ANTIGRAVITY_GROUP_LABEL_KEYS.get(normalizeLabelKey(groupLabel));
      for (const bucket of group.buckets ?? []) {
        const bucketLabel = (bucket.label ?? '').trim();
        const bucketKey = ANTIGRAVITY_BUCKET_LABEL_KEYS.get(normalizeLabelKey(bucketLabel));
        push({
          id: `${group.id ?? groupLabel}:${bucket.id ?? bucketLabel}`,
          label: bucketKey ? undefined : bucketLabel || undefined,
          labelKey:
            bucketKey ??
            (isFiniteNumber(bucket.resetAtMs)
              ? periodLabelKey(bucket.periodHours, bucket.resetAtMs, nowMs)
              : undefined),
          prefix: prefixKey ? undefined : groupLabel || undefined,
          prefixKey,
          remainingPercent: isFiniteNumber(bucket.remainingFraction)
            ? Math.min(100, Math.max(0, bucket.remainingFraction * 100))
            : null,
          resetAtMs: bucket.resetAtMs,
          periodHours: bucket.periodHours,
        });
      }
    }
  } else if (provider === 'kimi' || provider === 'kiro') {
    const kimiRows = (quota as { rows?: KimiRowShape[] }).rows ?? [];
    kimiRows.forEach((row, index) => {
      const used = isFiniteNumber(row.used) ? row.used : null;
      const limit = isFiniteNumber(row.limit) ? row.limit : null;
      const remainingPercent =
        limit !== null && limit > 0 && used !== null
          ? Math.min(100, Math.max(0, ((limit - used) / limit) * 100))
          : null;
      push({
        id: row.id || `row-${index}`,
        label: row.label,
        labelKey: row.labelKey,
        labelParams: row.labelParams,
        remainingPercent,
        resetAtMs: row.resetAtMs,
        periodHours: row.periodHours,
      });
    });
  } else if (provider === 'xai') {
    const billing = (
      quota as {
        billing?: {
          periodType?: string;
          usagePercent?: number | null;
          usedPercent?: number | null;
          monthlyLimitCents?: number | null;
          planLabel?: string;
          resetAtMs?: number | null;
          billingPeriodEnd?: string;
        } | null;
      }
    ).billing;
    if (billing) {
      // resetAtMs is the active period's end; the xAI summary keeps it honest
      // for both period types (weekly rate limit vs monthly billing rollover).
      if (billing.periodType === 'weekly' || billing.periodType === 'monthly') {
        const isMonthly = billing.periodType === 'monthly';
        // A bare rollover with no monthly quota figure is not an expiring
        // quota — nothing is lost when it resets.
        const hasMonthlyQuota = billing.usedPercent !== null || billing.monthlyLimitCents !== null;
        if (!isMonthly || hasMonthlyQuota) {
          push({
            id: `xai:${billing.periodType}`,
            labelKey: isMonthly
              ? 'sharing.quota_expiry_period_monthly'
              : 'sharing.quota_expiry_period_weekly',
            prefix: billing.planLabel,
            remainingPercent: remainingFromUsed(
              isMonthly ? (billing.usedPercent ?? billing.usagePercent) : billing.usagePercent
            ),
            resetAtMs: billing.resetAtMs,
            periodHours: isMonthly ? 24 * 30 : WEEKLY_PERIOD_HOURS,
          });
        }
      }
      // A weekly summary can still carry a monthly spend cap that rolls over
      // at billingPeriodEnd — a second expiring row worth listing.
      if (billing.periodType === 'weekly') {
        const monthlyResetAtMs = parseIsoToMs(billing.billingPeriodEnd);
        const hasMonthlyQuota = billing.usedPercent !== null || billing.monthlyLimitCents !== null;
        if (hasMonthlyQuota && monthlyResetAtMs !== null) {
          push({
            id: 'xai:monthly-billing',
            labelKey: 'sharing.quota_expiry_period_monthly',
            prefix: billing.planLabel,
            remainingPercent: remainingFromUsed(billing.usedPercent),
            resetAtMs: monthlyResetAtMs,
            periodHours: 24 * 30,
          });
        }
      }
    }
  } else if (provider === 'devin') {
    const windows = (quota as { windows?: WindowShape[] }).windows ?? [];
    windows
      .filter((window) => window.id === 'weekly')
      .forEach((window, index) =>
        push({
          id: window.id || `weekly-${index}`,
          label: window.label,
          labelKey: window.labelKey,
          remainingPercent: isFiniteNumber(window.remainingPercent)
            ? Math.min(100, Math.max(0, window.remainingPercent))
            : null,
          resetAtMs: window.resetAtMs,
          periodHours: window.periodHours ?? WEEKLY_PERIOD_HOURS,
        })
      );
  } else if (provider === 'meta') {
    const data = (
      quota as {
        data?: {
          planName?: string;
          windows?: { id?: string; usedPercent?: number | null; resetAt?: number }[];
        };
      }
    ).data;
    (data?.windows ?? [])
      .filter((window) => window.id === 'weekly')
      .forEach((window, index) =>
        push({
          id: window.id || `weekly-${index}`,
          labelKey: 'sharing.quota_expiry_period_weekly',
          prefix: data?.planName,
          remainingPercent: remainingFromUsed(window.usedPercent),
          resetAtMs: isFiniteNumber(window.resetAt) ? window.resetAt * 1000 : null,
          periodHours: WEEKLY_PERIOD_HOURS,
        })
      );
  }

  return rows.sort((a, b) => a.resetAtMs - b.resetAtMs);
}

/**
 * Credentials with at least one expiring weekly/monthly row, sorted soonest
 * first. Rows inside an entry are already sorted by the collector.
 */
export function buildExpiringEntries(
  entries: QuotaFileEntry[],
  getQuota: (entry: QuotaFileEntry) => QuotaCardState | undefined,
  nowMs: number
): ExpiringQuotaEntry[] {
  return entries
    .map((entry): ExpiringQuotaEntry | null => {
      const rows = collectExpiringQuotaRows(entry.type, getQuota(entry), nowMs);
      return rows.length === 0 ? null : { entry, rows, soonestAtMs: rows[0].resetAtMs };
    })
    .filter((item): item is ExpiringQuotaEntry => item !== null)
    .sort((a, b) => a.soonestAtMs - b.soonestAtMs);
}
