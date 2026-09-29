/**
 * Quota windows timeline: lane derivation and window projection.
 *
 * Pure functions over board entries — no React, no clock of its own (`now` is
 * always passed in), so every case here is directly testable.
 *
 * The chart answers one question the cards can't: *when does capacity come
 * back, and does it come back all at once?* Four credentials all resetting on
 * the same evening is a very different situation from four staggered across a
 * week, and no per-card percentage shows that.
 */

import { DAY_MS, HOUR_MS } from '@/utils/time/durations';
import type { QuotaProviderType } from './providers/types';

export { DAY_MS, HOUR_MS };

/** Weekly view spans a fortnight; the session view zooms to three days. */
export type TimelineMode = 'weekly' | 'session';

export const TIMELINE_SPAN_DAYS: Record<TimelineMode, number> = {
  weekly: 14,
  session: 3,
};

/** The rolling window the session view projects, in hours. */
const SESSION_PERIOD_HOURS = 5;

/** A limit summarized in the lane's left column. */
export interface TimelineLimit {
  label: string;
  /** Remaining percent, 0..100. */
  remaining: number;
}

/** A manual quota-reset credit attached to a Codex credential. */
export interface TimelineResetCredit {
  id: string;
  grantedAtMs: number | null;
  expiresAtMs: number;
}

/** A reset-credit expiry projected onto the visible span. */
export interface TimelineResetCreditMark extends TimelineResetCredit {
  leftPercent: number;
}

/** One credential's row in the chart. */
export interface TimelineLane {
  name: string;
  displayName: string;
  provider: QuotaProviderType;
  /** Instant a window boundary falls on; all other boundaries derive from it. */
  anchorMs: number | null;
  /** Window length in hours. */
  periodHours: number | null;
  /** Remaining percent reported for the window ending at `anchorMs`. */
  remaining: number | null;
  limits: TimelineLimit[];
  resetCredits: TimelineResetCredit[];
}

/** One drawn bar: a single window occurrence within the visible span. */
export interface TimelineWindow {
  startMs: number;
  endMs: number;
  /** Fractions of the span, 0..100, already clipped to the visible range. */
  leftPercent: number;
  widthPercent: number;
  state: 'past' | 'live' | 'next';
  /** Remaining percent only when this is the API-reported current window. */
  remaining: number | null;
}

/**
 * Every window boundary of `periodMs` aligned to `anchorMs`, covering
 * [fromMs, toMs].
 *
 * The anchor is a known *reset* instant, so windows are projected backwards and
 * forwards from it by whole periods. Both directions matter: the visible span
 * usually starts before the current window opened, and the point of the chart
 * is what's coming.
 */
export function windowsIn(
  anchorMs: number,
  periodMs: number,
  fromMs: number,
  toMs: number
): { startMs: number; endMs: number }[] {
  if (!Number.isFinite(anchorMs) || !(periodMs > 0)) return [];
  if (!(toMs > fromMs)) return [];

  // Guard against a pathological period (a bad payload) turning this into a
  // multi-million-iteration loop.
  const maxWindows = Math.ceil((toMs - fromMs) / periodMs) + 2;
  if (maxWindows > 1000) return [];

  let end = anchorMs + Math.ceil((fromMs - anchorMs) / periodMs) * periodMs;
  const out: { startMs: number; endMs: number }[] = [];
  while (end - periodMs < toMs) {
    out.push({ startMs: end - periodMs, endMs: end });
    end += periodMs;
  }
  return out;
}

/** Start of the local day containing `ms`. */
export function startOfDay(ms: number): number {
  const d = new Date(ms);
  d.setHours(0, 0, 0, 0);
  return d.getTime();
}

/** Start of the local week (Sunday) containing `ms`. */
export function startOfWeek(ms: number): number {
  const d = new Date(startOfDay(ms));
  d.setDate(d.getDate() - d.getDay());
  return d.getTime();
}

/**
 * Visible span for a mode and offset.
 *
 * Weekly steps a week at a time from the containing Sunday; session steps a day
 * at a time from today. Uses date arithmetic rather than adding fixed
 * millisecond counts so a DST transition inside the span doesn't shift every
 * subsequent day by an hour.
 */
export function timelineSpan(
  mode: TimelineMode,
  offset: number,
  now: number
): { startMs: number; endMs: number; days: number } {
  const days = TIMELINE_SPAN_DAYS[mode];
  const base = new Date(mode === 'weekly' ? startOfWeek(now) : startOfDay(now));
  base.setDate(base.getDate() + offset * (mode === 'weekly' ? 7 : 1));
  const startMs = base.getTime();

  const end = new Date(startMs);
  end.setDate(end.getDate() + days);

  return { startMs, endMs: end.getTime(), days };
}

/**
 * Project one lane's windows onto a span, clipped and positioned.
 *
 * Windows falling entirely outside the span are dropped rather than returned
 * with a zero width, so a caller can treat an empty result as "nothing to draw".
 */
export function projectLane(
  lane: TimelineLane,
  spanStartMs: number,
  spanEndMs: number,
  now: number,
  mode: TimelineMode
): TimelineWindow[] {
  const periodHours = lane.periodHours;
  if (lane.anchorMs === null || !periodHours) return [];
  if (mode === 'session' && periodHours !== SESSION_PERIOD_HOURS) return [];

  const span = spanEndMs - spanStartMs;
  if (span <= 0) return [];

  const toPercent = (ms: number) => ((ms - spanStartMs) / span) * 100;

  return windowsIn(lane.anchorMs, periodHours * HOUR_MS, spanStartMs, spanEndMs)
    .map((window): TimelineWindow | null => {
      const left = Math.max(0, toPercent(window.startMs));
      const right = Math.min(100, toPercent(window.endMs));
      if (right <= 0 || left >= 100 || right <= left) return null;

      const state: TimelineWindow['state'] =
        window.endMs <= now ? 'past' : window.startMs <= now ? 'live' : 'next';

      return {
        startMs: window.startMs,
        endMs: window.endMs,
        leftPercent: left,
        widthPercent: right - left,
        state,
        // `lane.remaining` belongs to the current payload window ending at the
        // anchor. Once that reset passes, projected windows must not reuse it.
        remaining: state === 'live' && window.endMs === lane.anchorMs ? lane.remaining : null,
      };
    })
    .filter((window): window is TimelineWindow => window !== null);
}

/** Project unexpired reset-credit expiry instants onto the visible span. */
export function projectResetCredits(
  lane: TimelineLane,
  spanStartMs: number,
  spanEndMs: number,
  now: number
): TimelineResetCreditMark[] {
  const span = spanEndMs - spanStartMs;
  if (span <= 0) return [];

  return lane.resetCredits
    .filter(
      (credit) =>
        credit.expiresAtMs > now &&
        credit.expiresAtMs >= spanStartMs &&
        credit.expiresAtMs < spanEndMs
    )
    .map((credit) => ({
      ...credit,
      leftPercent: ((credit.expiresAtMs - spanStartMs) / span) * 100,
    }));
}

/**
 * Pick the window a lane is drawn from: the one whose period best fits the
 * visible span, tie-broken by the soonest reset.
 *
 * A credential usually has several (5-hour, 7-day, per-model). Picking the
 * soonest reset outright looks right and renders uselessly: across a fortnight
 * the 5-hour window always resets first, so every lane becomes ~67 slivers
 * instead of two readable weekly bars. The long window is what a two-week view
 * is *for*; the session view exists precisely to see the short one.
 *
 * `maxPeriodHours` bounds what counts as fitting — the caller passes the span.
 * With nothing under the bound, the shortest available window is used rather
 * than drawing nothing.
 */
export function pickLaneWindow<
  T extends { resetAtMs?: number | null; periodHours?: number | null },
>(windows: readonly T[], maxPeriodHours?: number): T | null {
  const usable = windows.filter(
    (window) => typeof window.resetAtMs === 'number' && Number.isFinite(window.resetAtMs)
  );
  if (usable.length === 0) return null;

  const periodOf = (window: T) =>
    typeof window.periodHours === 'number' && window.periodHours > 0 ? window.periodHours : 0;

  const fitting =
    maxPeriodHours === undefined
      ? usable
      : usable.filter((window) => periodOf(window) <= maxPeriodHours);

  // Longest period that still fits; soonest reset breaks a tie.
  const pool = fitting.length > 0 ? fitting : usable;
  return pool.reduce((best, window) => {
    const byPeriod = periodOf(window) - periodOf(best);
    if (byPeriod !== 0) return byPeriod > 0 ? window : best;
    return (window.resetAtMs as number) < (best.resetAtMs as number) ? window : best;
  });
}

/**
 * Whether a lane has anything to draw, in any mode.
 *
 * Without an anchor no bar can be projected at any span or zoom, so the row
 * would be permanently blank. The card grid above already enumerates every
 * credential, so a blank row here adds no information — it just makes the
 * chart taller and the real lanes harder to compare against each other.
 *
 * Note this is about the lane, not the current view: an anchored lane whose
 * windows fall outside the visible span still gets a row, and says so.
 */
export function laneHasWindow(lane: TimelineLane): boolean {
  return lane.anchorMs !== null;
}

/* ------------------------------------------------------------------ lanes */

/** Shape the lane builder reads. Deliberately structural — see the note below. */
interface WindowLike {
  id?: string;
  label?: string;
  usedPercent?: number | null;
  resetAtMs?: number | null;
  periodHours?: number | null;
}

interface ResetCreditLike {
  id?: string;
  status?: string;
  grantedAt?: string;
  expiresAt?: string;
}

interface KimiRowLike {
  label?: string;
  labelKey?: string;
  used: number;
  limit: number;
  resetAtMs?: number | null;
  periodHours?: number | null;
}

interface XaiBillingLike {
  periodType?: string;
  usagePercent?: number | null;
  resetAtMs?: number | null;
  periodHours?: number | null;
  productUsage?: { product?: string; usagePercent?: number | null }[];
}

interface AntigravityBucketLike {
  label?: string;
  /** Fraction 0..1 of quota REMAINING — the inverse of the percent-used providers. */
  remainingFraction?: number | null;
  resetAtMs?: number | null;
  periodHours?: number | null;
}

interface MetaWindowLike {
  id: 'window' | 'weekly';
  usedPercent: number | null;
  /** Unix seconds from the upstream Meta contract. */
  resetAt?: number;
  durationMinutes?: number;
}

export interface TimelineLaneInput {
  name: string;
  displayName: string;
  provider: QuotaProviderType;
  quota: { status?: string } | undefined;
  /**
   * Longest window period worth drawing, in hours — normally the visible span.
   * A window longer than the whole view can't show a boundary, and a much
   * shorter one degenerates into slivers.
   */
  maxPeriodHours?: number;
}

const clampPercent = (value: number) => Math.min(100, Math.max(0, value));

/**
 * Build a lane for one credential.
 *
 * Read structurally per provider rather than through a normalized model: the
 * five state shapes disagree about where a window lives and what its percentage
 * means, and flattening them would lose exactly the detail the chart needs.
 *
 * Providers that expose no usable reset instant produce a lane with a null
 * anchor. That renders as an explicitly empty row rather than being dropped —
 * a missing credential reads as an oversight, an empty one reads as "nothing
 * scheduled", which is the truth.
 */
export function buildTimelineLane(input: TimelineLaneInput): TimelineLane {
  const { name, displayName, provider, quota, maxPeriodHours } = input;
  const empty: TimelineLane = {
    name,
    displayName,
    provider,
    anchorMs: null,
    periodHours: null,
    remaining: null,
    limits: [],
    resetCredits: [],
  };

  if (!quota || quota.status !== 'success') return empty;

  if (provider === 'claude' || provider === 'codex') {
    const windows = ((quota as { windows?: WindowLike[] }).windows ?? []).filter(
      (window) => typeof window.resetAtMs === 'number'
    );
    const preferredCodexId =
      maxPeriodHours !== undefined && maxPeriodHours <= SESSION_PERIOD_HOURS
        ? 'five-hour'
        : 'weekly';
    // Codex can report model-scoped windows with the same period as the account
    // window (for example GPT-5.3-Codex-Spark weekly). A reset-time tie-break
    // would make the lane silently switch to that model's quota. Keep the lane
    // anchored to the standard account window whenever it fits this view.
    const preferredCodexWindow =
      provider === 'codex'
        ? windows.find(
            (window) =>
              window.id === preferredCodexId &&
              typeof window.periodHours === 'number' &&
              window.periodHours > 0 &&
              (maxPeriodHours === undefined || window.periodHours <= maxPeriodHours)
          )
        : undefined;
    const chosen = preferredCodexWindow ?? pickLaneWindow(windows, maxPeriodHours);
    if (!chosen) return empty;

    const resetCredits =
      provider === 'codex'
        ? ((quota as { rateLimitResetCredits?: ResetCreditLike[] }).rateLimitResetCredits ?? [])
            .filter((credit) => credit.status === 'available')
            .map((credit): TimelineResetCredit | null => {
              const expiresAtMs = new Date(credit.expiresAt ?? '').getTime();
              if (!Number.isFinite(expiresAtMs)) return null;

              const grantedAtMs = new Date(credit.grantedAt ?? '').getTime();
              return {
                id: credit.id ?? '',
                grantedAtMs: Number.isFinite(grantedAtMs) ? grantedAtMs : null,
                expiresAtMs,
              };
            })
            .filter((credit): credit is TimelineResetCredit => credit !== null)
        : [];

    return {
      ...empty,
      anchorMs: chosen.resetAtMs ?? null,
      periodHours: chosen.periodHours ?? null,
      // Claude and Codex store percent USED.
      remaining:
        typeof chosen.usedPercent === 'number' ? clampPercent(100 - chosen.usedPercent) : null,
      limits: windows
        .filter((window) => typeof window.usedPercent === 'number')
        .map((window) => ({
          label: window.label ?? '',
          remaining: clampPercent(100 - (window.usedPercent as number)),
        })),
      resetCredits,
    };
  }

  if (provider === 'devin') {
    const windows =
      (
        quota as {
          windows?: {
            id: string;
            label?: string;
            remainingPercent: number | null;
            resetAtMs: number | null;
            periodHours: number;
          }[];
        }
      ).windows ?? [];
    const chosen = pickLaneWindow(windows, maxPeriodHours);
    if (!chosen) return empty;
    return {
      ...empty,
      anchorMs: chosen.resetAtMs,
      periodHours: chosen.periodHours,
      remaining: chosen.remainingPercent,
      limits: windows
        .filter((window) => window.remainingPercent !== null)
        .map((window) => ({
          label: window.label ?? window.id,
          remaining: window.remainingPercent as number,
        })),
    };
  }

  if (provider === 'xai') {
    const billing = (quota as { billing?: XaiBillingLike | null }).billing;
    // Only the weekly limit is a quota window. The monthly figure on the same
    // summary is a billing cycle — a spend cap rolling over, not rate-limited
    // capacity coming back — so an account without a weekly limit contributes
    // no lane rather than a lane that means something different from the rest.
    if (!billing || billing.periodType !== 'weekly') return empty;
    if (typeof billing.resetAtMs !== 'number' || !Number.isFinite(billing.resetAtMs)) return empty;

    const remaining =
      typeof billing.usagePercent === 'number' ? clampPercent(100 - billing.usagePercent) : null;

    return {
      ...empty,
      anchorMs: billing.resetAtMs,
      // A payload that states an end without a start can't derive its own
      // length; weekly is what `periodType` already told us.
      periodHours: billing.periodHours ?? 24 * 7,
      remaining,
      // Per-product usage is the closest analogue to the other providers'
      // per-window breakdown.
      limits: (billing.productUsage ?? [])
        .map((entry) => ({
          label: entry.product ?? '',
          remaining:
            typeof entry.usagePercent === 'number' ? clampPercent(100 - entry.usagePercent) : null,
        }))
        .filter((limit): limit is TimelineLimit => limit.remaining !== null),
    };
  }

  if (provider === 'antigravity') {
    // Buckets live one level down, inside groups, and the groups are a display
    // concern the chart doesn't care about — flatten them.
    const buckets = ((quota as { groups?: { buckets?: AntigravityBucketLike[] }[] }).groups ?? [])
      .flatMap((group) => group.buckets ?? [])
      .filter((bucket) => typeof bucket.resetAtMs === 'number');
    const chosen = pickLaneWindow(buckets, maxPeriodHours);
    if (!chosen) return empty;

    // Antigravity reports the fraction REMAINING, not percent used.
    const remainingOf = (bucket: AntigravityBucketLike) =>
      typeof bucket.remainingFraction === 'number'
        ? clampPercent(Math.round(bucket.remainingFraction * 100))
        : null;

    return {
      ...empty,
      anchorMs: chosen.resetAtMs ?? null,
      periodHours: chosen.periodHours ?? null,
      remaining: remainingOf(chosen),
      limits: buckets
        .map((bucket) => ({ label: bucket.label ?? '', remaining: remainingOf(bucket) }))
        .filter((limit): limit is TimelineLimit => limit.remaining !== null),
    };
  }

  if (provider === 'kimi') {
    const rows = ((quota as { rows?: KimiRowLike[] }).rows ?? []).filter(
      (row) => typeof row.resetAtMs === 'number'
    );
    const chosen = pickLaneWindow(rows, maxPeriodHours);
    if (!chosen) return empty;

    // Kimi reports raw counts; remaining is derived.
    const remainingOf = (row: KimiRowLike) =>
      row.limit > 0 ? clampPercent(Math.round(((row.limit - row.used) / row.limit) * 100)) : null;

    return {
      ...empty,
      anchorMs: chosen.resetAtMs ?? null,
      periodHours: chosen.periodHours ?? null,
      remaining: remainingOf(chosen),
      limits: rows
        .map((row) => ({ label: row.label ?? '', remaining: remainingOf(row) }))
        .filter((limit): limit is TimelineLimit => limit.remaining !== null),
    };
  }

  if (provider === 'meta') {
    const sourceWindows = (quota as { data?: { windows?: MetaWindowLike[] } }).data?.windows ?? [];
    const windows = sourceWindows
      // The upstream weekly bucket has no duration field; its scope defines seven days.
      .map((window) => ({
        ...window,
        durationMinutes: window.id === 'weekly' ? 7 * 24 * 60 : window.durationMinutes,
      }))
      .filter(
        (window) =>
          typeof window.resetAt === 'number' &&
          Number.isFinite(window.resetAt) &&
          typeof window.durationMinutes === 'number' &&
          Number.isFinite(window.durationMinutes) &&
          window.durationMinutes > 0
      )
      .map((window) => ({
        ...window,
        resetAtMs: (window.resetAt as number) * 1000,
        periodHours: (window.durationMinutes as number) / 60,
      }));
    const chosen = pickLaneWindow(windows, maxPeriodHours);
    if (!chosen) return empty;

    const remainingOf = (window: MetaWindowLike) =>
      typeof window.usedPercent === 'number' && Number.isFinite(window.usedPercent)
        ? clampPercent(100 - window.usedPercent)
        : null;

    return {
      ...empty,
      anchorMs: chosen.resetAtMs,
      periodHours: chosen.periodHours,
      remaining: remainingOf(chosen),
      limits: windows
        .map((window) => ({
          label: `meta_quota.${window.id}`,
          remaining: remainingOf(window),
        }))
        .filter((limit): limit is TimelineLimit => limit.remaining !== null),
    };
  }

  return empty;
}
