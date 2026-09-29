/**
 * When each row on a quota card gets its capacity back.
 *
 * A card lists several limits at once — a 5-hour window, a weekly one, a
 * handful of manual reset credits — and the only one that governs what you can
 * do next is whichever recovers first. Reading five timestamps to find it is
 * the work this moves off the user.
 *
 * Pure and React-free: `nowMs` is passed in, the quota state is read
 * structurally, and nothing here imports the store.
 *
 * Related but deliberately separate from `buildTimelineLane` in
 * quotaTimelineModel.ts. That reads the same five shapes to pick *one* window
 * per credential — the longest that fits the visible span, because a fortnight
 * drawn from 5-hour windows is 67 unreadable slivers. This wants *every*
 * instant and the *soonest* of them. Same inputs, opposite selection rules;
 * merging them would mean one function with a mode flag and two sets of pinned
 * tests fighting each other.
 */

import { parseIsoToMs } from '@/utils/quota';
import { HOUR_MS } from '@/utils/time/durations';
import type { QuotaProviderType } from './providers/types';

export interface QuotaRowInstant {
  /** Matches the React key of the row it belongs to. */
  rowId: string;
  atMs: number;
  kind: 'window' | 'credit';
}

/**
 * Row identity for a Codex reset credit.
 *
 * Exported so `CodexQuotaBody` can use one expression for both its React key
 * and its highlight comparison. Two copies of `credit.id || fallback` that
 * drift apart would put the emphasis on the wrong row, which is worse than no
 * emphasis at all.
 */
export function resetCreditRowId(
  credit: { id?: string; expiresAt?: string },
  index: number
): string {
  return credit.id || `${credit.expiresAt}-${index}`;
}

/* Structural shapes — the five provider states disagree about where a window
   lives, so each is read on its own terms rather than through a lossy
   normalized model. */

interface WindowLike {
  id?: string;
  resetAtMs?: number | null;
}

interface ResetCreditLike {
  id?: string;
  status?: string;
  expiresAt?: string;
}

/** Row id used by the xAI weekly limit, which has no id of its own. */
export const XAI_WEEKLY_ROW_ID = 'xai:weekly';

const isUsableMs = (value: unknown): value is number =>
  typeof value === 'number' && Number.isFinite(value);

const collectRows = (rows: readonly WindowLike[], fallbackPrefix: string): QuotaRowInstant[] =>
  rows
    .map((row, index): QuotaRowInstant | null =>
      isUsableMs(row.resetAtMs)
        ? { rowId: row.id || `${fallbackPrefix}-${index}`, atMs: row.resetAtMs, kind: 'window' }
        : null
    )
    .filter((instant): instant is QuotaRowInstant => instant !== null);

/**
 * Every recovery instant on one credential, tagged with the row it belongs to.
 *
 * Only genuine capacity-return events are collected. The Codex subscription
 * renewal date and xAI's monthly billing rollover are excluded: a spend cap
 * turning over is not a rate limit lifting, and ranking cards by it would
 * answer a different question than the one being asked. Both still render
 * their own countdown.
 */
export function collectQuotaRowInstants(
  provider: QuotaProviderType,
  quota: unknown
): QuotaRowInstant[] {
  const state = quota as { status?: string } | undefined;
  if (!state || state.status !== 'success') return [];

  if (provider === 'claude' || provider === 'codex' || provider === 'devin') {
    const windows = collectRows((quota as { windows?: WindowLike[] }).windows ?? [], 'window');
    if (provider !== 'codex') return windows;

    const credits = (
      (quota as { rateLimitResetCredits?: ResetCreditLike[] }).rateLimitResetCredits ?? []
    )
      .map((credit, index): QuotaRowInstant | null => {
        if (credit.status !== 'available') return null;
        const atMs = parseIsoToMs(credit.expiresAt);
        return atMs === null
          ? null
          : { rowId: resetCreditRowId(credit, index), atMs, kind: 'credit' };
      })
      .filter((instant): instant is QuotaRowInstant => instant !== null);

    return [...windows, ...credits];
  }

  if (provider === 'xai') {
    const billing = (
      quota as { billing?: { periodType?: string; resetAtMs?: number | null } | null }
    ).billing;
    if (!billing || billing.periodType !== 'weekly' || !isUsableMs(billing.resetAtMs)) return [];
    return [{ rowId: XAI_WEEKLY_ROW_ID, atMs: billing.resetAtMs, kind: 'window' }];
  }

  if (provider === 'antigravity') {
    // Buckets live inside groups; the grouping is a display concern here.
    const buckets = ((quota as { groups?: { buckets?: WindowLike[] }[] }).groups ?? []).flatMap(
      (group) => group.buckets ?? []
    );
    return collectRows(buckets, 'bucket');
  }

  if (provider === 'kimi') {
    return collectRows((quota as { rows?: WindowLike[] }).rows ?? [], 'row');
  }

  if (provider === 'meta') {
    const windows =
      (
        quota as {
          data?: {
            windows?: {
              id: 'window' | 'weekly';
              usedPercent: number | null;
              resetAt?: number;
            }[];
          };
        }
      ).data?.windows ?? [];

    // A reset is a recovery event only when the current window has consumed
    // capacity. In particular, 0% used must not make an otherwise healthy Meta
    // credential sort ahead of credentials that are actually blocked. Unknown
    // usage is not treated as 100% used either.
    return windows
      .filter(
        (window) =>
          typeof window.usedPercent === 'number' &&
          Number.isFinite(window.usedPercent) &&
          window.usedPercent > 0 &&
          isUsableMs(window.resetAt)
      )
      .map((window) => ({
        rowId: window.id,
        atMs: (window.resetAt as number) * 1000,
        kind: 'window' as const,
      }));
  }

  return [];
}

/**
 * The row that recovers first, or null when nothing is still pending.
 *
 * Instants at or before `now` are ignored — a window that already reset must
 * not keep the emphasis. Ties break on row id so the choice is deterministic
 * rather than dependent on collection order.
 */
export function pickSoonestRowId(
  instants: readonly QuotaRowInstant[],
  nowMs: number
): string | null {
  let best: QuotaRowInstant | null = null;
  for (const instant of instants) {
    if (instant.atMs <= nowMs) continue;
    if (
      best === null ||
      instant.atMs < best.atMs ||
      (instant.atMs === best.atMs && instant.rowId < best.rowId)
    ) {
      best = instant;
    }
  }
  return best?.rowId ?? null;
}

/**
 * The recovery row whose countdown should receive warning emphasis.
 *
 * A nearest reset that is still hours or days away is informational, not
 * urgent. Only the final hour is highlighted, and exactly one hour remaining
 * is deliberately excluded to match the "less than one hour" UI rule.
 */
export function pickUrgentRowId(
  instants: readonly QuotaRowInstant[],
  nowMs: number
): string | null {
  return pickSoonestRowId(
    instants.filter((instant) => instant.atMs - nowMs > 0 && instant.atMs - nowMs < HOUR_MS),
    nowMs
  );
}

/**
 * Soonest upcoming recovery instant for a whole credential — the sort key for
 * "soonest recovery first". Null when nothing is loaded or nothing is pending.
 */
export function nextRecoveryMs(
  provider: QuotaProviderType,
  quota: unknown,
  nowMs: number
): number | null {
  let best: number | null = null;
  for (const instant of collectQuotaRowInstants(provider, quota)) {
    if (instant.atMs <= nowMs) continue;
    if (best === null || instant.atMs < best) best = instant.atMs;
  }
  return best;
}
