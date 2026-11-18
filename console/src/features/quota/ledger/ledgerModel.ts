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
  /** The weekly (or longer) reset reset-first routing ranks by; null when not reported. */
  weeklyResetAtMs: number | null;
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

/* The window papa-proxy's reset-first selector ranks by: Claude's shared weekly
 * limit, and Codex's longest window. Mirrors sdk/cliproxy/auth/selector_reset_first.go. */
const CLAUDE_ROUTING = ['seven-day', 'seven-day-fable'];
const CODEX_ROUTING = ['monthly', 'weekly'];

const routingResetOf = (provider: LedgerProvider, windows: WindowLike[]): number | null => {
  const ids = provider === 'claude' ? CLAUDE_ROUTING : CODEX_ROUTING;
  for (const id of ids) {
    const at = windows.find((w) => w.id === id)?.resetAtMs;
    if (typeof at === 'number' && Number.isFinite(at)) return at;
  }
  return null;
};

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
 * soonest weekly reset first, unknown resets last, then by file name.
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
        weeklyResetAtMs:
          status === 'success' ? routingResetOf(provider, quota.windows ?? []) : null,
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
    const at = row.weeklyResetAtMs;
    return at !== null && at > nowMs ? at : Number.POSITIVE_INFINITY;
  };
  rows.sort((a, b) => {
    const diff = resetKey(a) - resetKey(b);
    if (diff !== 0) return diff;
    return a.entry.file.name.localeCompare(b.entry.file.name);
  });

  // Any exhausted window (shared weekly, 5-hour) keeps routing off the account.
  const next = rows.find(
    (row) =>
      !row.entry.file.disabled &&
      row.primary !== null &&
      (row.primary.remaining ?? 0) > 0 &&
      row.meters.every((meter) => meter.remaining !== 0) &&
      resetKey(row) !== Number.POSITIVE_INFINITY
  );
  if (next) next.nextUp = true;
  return rows;
}

/** What a row has left of its weekly quota: the tighter of its weekly windows. */
export function weeklyRemaining(row: LedgerRow): number | null {
  const known = row.meters
    .filter((meter) => meter.id !== 'five-hour' && meter.remaining !== null)
    .map((meter) => meter.remaining as number);
  return known.length ? Math.min(...known) : null;
}

/** Aggregate a provider's rows into the summary card at the top of the ledger. */
export function buildLedgerSummary(provider: LedgerProvider, rows: LedgerRow[]): LedgerSummary {
  const loaded = rows.filter((row) => row.primary !== null);
  const primaryLabelKey = loaded[0]?.primary?.labelKey ?? null;
  const known = loaded.filter((row) => row.primary?.remaining !== null);
  const remainingTotal = known.length
    ? known.reduce((sum, row) => sum + (row.primary?.remaining ?? 0), 0)
    : null;

  return {
    provider,
    credentialCount: rows.length,
    primaryLabelKey,
    remainingTotal,
    capacity: known.length * 100,
  };
}

/** Auth file name prefix before the email: `claude-`, `claude-<hash>-`, `codex-<id>-`. */
const FILE_PREFIX =
  /^(?:claude|codex|gemini|vertex|aistudio|antigravity|xai|devin|kimi|meta|qwen|iflow)-(?:[0-9a-f]{8}-)?/;

/**
 * Hide the identifying parts of every email in a credential name:
 * `claude-theo@ping.gg.json` → `claude-t•••@p•••.gg.json`.
 * Only the auth file's own prefix is kept; the rest of the local part is masked,
 * hyphens included.
 */
export function maskCredentialName(name: string): string {
  return name.replace(
    /([A-Za-z0-9._%+-]+)@([A-Za-z0-9][A-Za-z0-9-]*)((?:\.[A-Za-z0-9-]+)*)/g,
    (_match, local: string, domain: string, rest: string, offset: number) => {
      const prefix = offset === 0 ? (local.match(FILE_PREFIX)?.[0] ?? '') : '';
      const user = local.slice(prefix.length);
      return `${prefix}${user ? user[0] : ''}•••@${domain[0]}•••${rest}`;
    }
  );
}
