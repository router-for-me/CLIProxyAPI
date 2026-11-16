import {
  QUOTA_SORT_MODES,
  QUOTA_TAB_ORDER,
  type QuotaSortMode,
  type QuotaTabId,
} from '@/features/quota/constants';
import {
  BURN_WINDOWS,
  FUTURE_USAGES,
  POOL_METRICS,
  type BurnWindowHours,
  type FutureUsage,
  type PoolMetric,
} from '@/features/quota/pool/constants';

/** Quota page UI preferences, persisted per browser session (sessionStorage). */
export type QuotaUiState = {
  tab?: QuotaTabId;
  sortMode?: QuotaSortMode;
  metric?: PoolMetric;
  burnWindow?: BurnWindowHours;
  futureUsage?: FutureUsage;
  accounts?: boolean;
  showEmails?: boolean;
};

const QUOTA_UI_STATE_KEY = 'quotaPage.uiState';

const QUOTA_TAB_ID_SET = new Set<string>(['all', ...QUOTA_TAB_ORDER]);
const QUOTA_SORT_MODE_SET = new Set<string>(QUOTA_SORT_MODES);

export const isQuotaTabId = (value: unknown): value is QuotaTabId =>
  typeof value === 'string' && QUOTA_TAB_ID_SET.has(value);

export const isQuotaSortMode = (value: unknown): value is QuotaSortMode =>
  typeof value === 'string' && QUOTA_SORT_MODE_SET.has(value);

export const isPoolMetric = (value: unknown): value is PoolMetric =>
  POOL_METRICS.some((metric) => metric === value);

export const isBurnWindow = (value: unknown): value is BurnWindowHours =>
  BURN_WINDOWS.some((hours) => hours === value);

export const isFutureUsage = (value: unknown): value is FutureUsage =>
  FUTURE_USAGES.some((usage) => usage === value);

const booleanOrUndefined = (value: unknown): boolean | undefined =>
  typeof value === 'boolean' ? value : undefined;

export const readQuotaUiState = (): QuotaUiState | null => {
  if (typeof window === 'undefined') return null;
  try {
    const raw = window.sessionStorage.getItem(QUOTA_UI_STATE_KEY);

    if (!raw) return null;

    const parsed = JSON.parse(raw) as Record<keyof QuotaUiState, unknown> | null;

    if (!parsed || typeof parsed !== 'object') return null;

    return {
      tab: isQuotaTabId(parsed.tab) ? parsed.tab : undefined,
      sortMode: isQuotaSortMode(parsed.sortMode) ? parsed.sortMode : undefined,
      metric: isPoolMetric(parsed.metric) ? parsed.metric : undefined,
      burnWindow: isBurnWindow(parsed.burnWindow) ? parsed.burnWindow : undefined,
      futureUsage: isFutureUsage(parsed.futureUsage) ? parsed.futureUsage : undefined,
      accounts: booleanOrUndefined(parsed.accounts),
      showEmails: booleanOrUndefined(parsed.showEmails),
    };
  } catch {
    return null;
  }
};

/**
 * Merge into whatever is already stored.
 *
 * Callers write one preference at a time — the tab strip knows nothing about
 * the sort control — so a whole-object write would silently drop the other
 * fields every time one of them changed.
 */
export const writeQuotaUiState = (state: QuotaUiState) => {
  if (typeof window === 'undefined') return;
  try {
    const next = { ...readQuotaUiState(), ...state };
    window.sessionStorage.setItem(QUOTA_UI_STATE_KEY, JSON.stringify(next));
  } catch {
    // ignore
  }
};
