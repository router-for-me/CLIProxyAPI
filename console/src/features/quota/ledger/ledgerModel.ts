/**
 * Ledger view model: one row per credential, one meter per quota window that
 * decides routing, and one summary per provider.
 *
 * Pure and React-free so the ordering and masking rules can be unit tested.
 * The row order mirrors papa-proxy's reset-first routing: the account whose
 * main weekly window resets soonest is spent first, so it is listed first and
 * marked "next up" while it still has quota left.
 */

import type { QuotaProviderType } from '../providers/types';
import type { QuotaFileEntry } from '../logic';

/** Providers the ledger draws natively; the rest keep their quota cards. */
export const LEDGER_PROVIDERS = ['claude', 'codex'] as const;
export type LedgerProvider = (typeof LEDGER_PROVIDERS)[number];

export const isLedgerProvider = (type: QuotaProviderType): type is LedgerProvider =>
  (LEDGER_PROVIDERS as readonly string[]).includes(type);

export interface LedgerMeter {
  id: string;
  /** i18n key under `ledger.window_*`, or a raw label when the window is unknown. */
  labelKey: string;
  /** Percent of the window still available, 0-100; null when unknown. */
  remaining: number | null;
  resetAtMs: number | null;
  resetLabel: string;
  /** Shown de-emphasised: informative, but not what routing spends. */
  muted: boolean;
}

export interface LedgerRow {
  entry: QuotaFileEntry;
  key: string;
  status: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  meters: LedgerMeter[];
  /** The window routing spends first; null until quota has loaded. */
  primary: LedgerMeter | null;
  planType: string | null;
  renewsAt: string | number | null;
  resetCredits: { available: number; nextExpiresAt: string | null } | null;
  nextUp: boolean;
}

export interface LedgerSummary {
  provider: LedgerProvider;
  credentialCount: number;
  primaryLabelKey: string | null;
  /** Sum of remaining percent across loaded credentials. */
  remainingTotal: number | null;
  /** 100 per loaded credential. */
  capacity: number;
  /** One segment per credential, in ledger order; null while unknown. */
  segments: (number | null)[];
  soonestResetAtMs: number | null;
  soonestResetLabel: string;
  secondary: { labelKey: string; remainingTotal: number } | null;
}

interface WindowLike {
  id?: string;
  usedPercent?: number | null;
  resetAtMs?: number | null;
  resetLabel?: string;
}

interface QuotaLike {
  status?: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  windows?: WindowLike[];
  planType?: string | null;
  subscriptionActiveUntil?: string | number | null;
  rateLimitResetCreditsAvailableCount?: number | null;
  rateLimitResetCredits?: { status?: string; expiresAt?: string }[];
}

/* Primary window candidates per provider; the first one present is spent first. */
const CLAUDE_PRIMARY = ['seven-day-fable', 'seven-day'];
const CODEX_PRIMARY = ['weekly', 'monthly'];

const LABEL_KEYS: Record<string, string> = {
  'claude:seven-day-fable': 'ledger.window_seven_day_fable',
  'claude:seven-day': 'ledger.window_seven_day',
  'claude:five-hour': 'ledger.window_five_hour',
  'codex:weekly': 'ledger.window_weekly',
  'codex:monthly': 'ledger.window_monthly',
  'codex:five-hour': 'ledger.window_five_hour',
};

const remainingOf = (window: WindowLike): number | null => {
  const used = window.usedPercent;
  if (typeof used !== 'number' || !Number.isFinite(used)) return null;
  return Math.max(0, Math.min(100, 100 - used));
};

const toMeter = (provider: LedgerProvider, window: WindowLike, muted: boolean): LedgerMeter => ({
  id: window.id ?? '',
  labelKey: LABEL_KEYS[`${provider}:${window.id}`] ?? window.id ?? '',
  remaining: remainingOf(window),
  resetAtMs:
    typeof window.resetAtMs === 'number' && Number.isFinite(window.resetAtMs)
      ? window.resetAtMs
      : null,
  resetLabel: window.resetLabel ?? '',
  muted,
});

/** Pick the meters a ledger row shows, primary first. */
export function selectLedgerMeters(provider: LedgerProvider, windows: WindowLike[]): LedgerMeter[] {
  const byId = new Map(windows.filter((w) => w.id).map((w) => [w.id as string, w]));
  const meters: LedgerMeter[] = [];
  const primaryIds = provider === 'claude' ? CLAUDE_PRIMARY : CODEX_PRIMARY;
  const primaryId = primaryIds.find((id) => byId.has(id));
  if (primaryId) meters.push(toMeter(provider, byId.get(primaryId) as WindowLike, false));
  const fiveHour = byId.get('five-hour');
  if (fiveHour) meters.push(toMeter(provider, fiveHour, false));
  // Claude's shared weekly limit stays visible, greyed, beside the model-scoped one.
  if (provider === 'claude' && primaryId === 'seven-day-fable' && byId.has('seven-day')) {
    meters.push(toMeter(provider, byId.get('seven-day') as WindowLike, true));
  }
  return meters;
}

/**
 * Build ledger rows for one provider, ordered for reset-first routing:
 * soonest primary reset first, unknown resets last, then by file name.
 */
export function buildLedgerRows(
  provider: LedgerProvider,
  entries: QuotaFileEntry[],
  quotaFor: (entry: QuotaFileEntry) => unknown,
  nowMs: number
): LedgerRow[] {
  const rows = entries
    .filter((entry) => entry.type === provider)
    .map((entry): LedgerRow => {
      const quota = (quotaFor(entry) ?? {}) as QuotaLike;
      const status = quota.status ?? 'idle';
      const meters = status === 'success' ? selectLedgerMeters(provider, quota.windows ?? []) : [];
      const credits = (quota.rateLimitResetCredits ?? [])
        .filter((credit) => credit.status === 'available' && credit.expiresAt)
        .map((credit) => credit.expiresAt as string)
        .sort();
      const available = quota.rateLimitResetCreditsAvailableCount;
      return {
        entry,
        key: `${provider}:${entry.file.name}`,
        status,
        error: quota.error,
        meters,
        primary: meters.find((meter) => !meter.muted) ?? null,
        planType: quota.planType ?? null,
        renewsAt: quota.subscriptionActiveUntil ?? null,
        resetCredits:
          provider === 'codex' && typeof available === 'number'
            ? { available, nextExpiresAt: credits[0] ?? null }
            : null,
        nextUp: false,
      };
    });

  const resetKey = (row: LedgerRow) => {
    const at = row.primary?.resetAtMs;
    return typeof at === 'number' && at > nowMs ? at : Number.POSITIVE_INFINITY;
  };
  rows.sort((a, b) => {
    const diff = resetKey(a) - resetKey(b);
    if (diff !== 0) return diff;
    return a.entry.file.name.localeCompare(b.entry.file.name);
  });

  const next = rows.find(
    (row) =>
      !row.entry.file.disabled &&
      row.primary !== null &&
      (row.primary.remaining ?? 0) > 0 &&
      resetKey(row) !== Number.POSITIVE_INFINITY
  );
  if (next) next.nextUp = true;
  return rows;
}

/** Aggregate a provider's rows into the summary card at the top of the ledger. */
export function buildLedgerSummary(
  provider: LedgerProvider,
  rows: LedgerRow[],
  nowMs: number
): LedgerSummary {
  const loaded = rows.filter((row) => row.primary !== null);
  const primaryLabelKey = loaded[0]?.primary?.labelKey ?? null;
  const known = loaded.filter((row) => row.primary?.remaining !== null);
  const remainingTotal = known.length
    ? known.reduce((sum, row) => sum + (row.primary?.remaining ?? 0), 0)
    : null;

  let soonest: LedgerMeter | null = null;
  for (const row of loaded) {
    const meter = row.primary as LedgerMeter;
    if (meter.resetAtMs === null || meter.resetAtMs <= nowMs) continue;
    if (!soonest || meter.resetAtMs < (soonest.resetAtMs as number)) soonest = meter;
  }

  // The second meter on each row (Claude's shared weekly, or a 5-hour window).
  const secondaryMeters = loaded
    .map((row) => row.meters.find((meter) => meter !== row.primary && meter.id !== 'five-hour'))
    .filter((meter): meter is LedgerMeter => Boolean(meter) && meter?.remaining !== null);
  const secondary = secondaryMeters.length
    ? {
        labelKey: secondaryMeters[0].labelKey,
        remainingTotal: secondaryMeters.reduce((sum, meter) => sum + (meter.remaining ?? 0), 0),
      }
    : null;

  return {
    provider,
    credentialCount: rows.length,
    primaryLabelKey,
    remainingTotal,
    capacity: known.length * 100,
    segments: rows.map((row) => row.primary?.remaining ?? null),
    soonestResetAtMs: soonest?.resetAtMs ?? null,
    soonestResetLabel: soonest?.resetLabel ?? '',
    secondary,
  };
}

/**
 * Hide the identifying parts of every email in a credential name:
 * `claude-theo@ping.gg.json` → `claude-t•••@p•••.gg.json`.
 * A hyphenated prefix before the local part (`claude-`, `codex-<id>-`) is kept.
 */
export function maskCredentialName(name: string): string {
  return name.replace(
    /([A-Za-z0-9._%+-]+)@([A-Za-z0-9][A-Za-z0-9-]*)((?:\.[A-Za-z0-9-]+)*)/g,
    (_match, local: string, domain: string, rest: string) => {
      const cut = local.lastIndexOf('-') + 1;
      const user = local.slice(cut);
      return `${local.slice(0, cut)}${user ? user[0] : ''}•••@${domain[0]}•••${rest}`;
    }
  );
}
