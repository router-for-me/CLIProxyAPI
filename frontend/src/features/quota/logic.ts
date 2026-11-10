/** React-free quota classification, filtering, sorting and pagination. */

import type { AuthFileItem } from '@/types';
import { derivePinnedFileName } from '@/features/authFiles/identity';
import { getQuotaDisplayName } from '@/utils/quota/identity';
import { ANTIGRAVITY_CONFIG } from './providers/antigravity/data';
import { CLAUDE_CONFIG } from './providers/claude/data';
import { CODEX_CONFIG } from './providers/codex/data';
import { DEVIN_CONFIG } from './providers/devin/data';
import { KIMI_CONFIG } from './providers/kimi/data';
import { KIRO_CONFIG } from './providers/kiro/data';
import { QODER_CONFIG } from './providers/qoder/data';
import { META_CONFIG } from './providers/meta/data';
import { XAI_CONFIG } from './providers/xai/data';
import { ZCODE_CONFIG } from './providers/zcode/data';
import type { QuotaProviderType } from './providers/types';
import { QUOTA_TAB_ORDER, type QuotaSortMode, type QuotaTabId } from './constants';

const QUOTA_FILTER_MAP: Record<QuotaProviderType, (file: AuthFileItem) => boolean> = {
  antigravity: ANTIGRAVITY_CONFIG.filterFn,
  claude: CLAUDE_CONFIG.filterFn,
  codex: CODEX_CONFIG.filterFn,
  devin: DEVIN_CONFIG.filterFn,
  kimi: KIMI_CONFIG.filterFn,
  kiro: KIRO_CONFIG.filterFn,
  qoder: QODER_CONFIG.filterFn,
  meta: META_CONFIG.filterFn,
  xai: XAI_CONFIG.filterFn,
  zcode: ZCODE_CONFIG.filterFn,
};

export interface QuotaFileEntry {
  file: AuthFileItem;
  type: QuotaProviderType;
}

/** A refresh-all intent belongs to the session that requested a successful list read. */
export function canRefreshQuotaAfterList(
  requestedSession: number,
  currentSession: number,
  filesSession: number | null,
  hasError: boolean,
  disabled: boolean
): boolean {
  return (
    !disabled && !hasError && requestedSession === currentSession && filesSession === currentSession
  );
}

export const resolveQuotaProviderType = (file: AuthFileItem): QuotaProviderType | null =>
  QUOTA_TAB_ORDER.find((type) => QUOTA_FILTER_MAP[type](file)) ?? null;

/** Exclude unsupported/disabled files and preserve provider grouping order. */
export function classifyQuotaFiles(files: AuthFileItem[]): QuotaFileEntry[] {
  const groups = new Map<QuotaProviderType, QuotaFileEntry[]>(
    QUOTA_TAB_ORDER.map((type) => [type, []])
  );
  for (const file of files) {
    const type = resolveQuotaProviderType(file);
    if (!type) continue;
    groups.get(type)?.push({ file, type });
  }
  return QUOTA_TAB_ORDER.flatMap((type) => groups.get(type) ?? []);
}

export function filterEntriesByTab(entries: QuotaFileEntry[], tab: QuotaTabId): QuotaFileEntry[] {
  if (tab === 'all') return entries;
  return entries.filter((entry) => entry.type === tab);
}

/** Search public account identifiers only; account may contain an API key. */
export function filterEntriesBySearch(entries: QuotaFileEntry[], search: string): QuotaFileEntry[] {
  const query = search.trim().toLowerCase();
  if (!query) return entries;
  return entries.filter(({ file }) =>
    [file.name, file.email].some(
      (value) => typeof value === 'string' && value.toLowerCase().includes(query)
    )
  );
}

/**
 * Order the grid by whichever credential recovers first.
 *
 * The instant is injected rather than read here: quota lives in the store and
 * arrives asynchronously, and keeping this function store-free is what makes
 * the ordering rules directly testable.
 *
 * Credentials with no instant — not loaded yet, failed, or reporting no
 * upcoming reset — sink to the bottom rather than sorting as "now". They keep
 * their incoming provider-grouped order, so the unloaded tail still reads like
 * the default view instead of an arbitrary shuffle. Because loading is
 * click-to-fetch, that tail is most of the list until the user asks for data.
 *
 * The original index is the final tiebreak, making stability an asserted
 * property rather than an assumption about the engine's sort.
 */
export function sortQuotaEntries(
  entries: QuotaFileEntry[],
  mode: QuotaSortMode,
  resolveNextRecoveryMs: (entry: QuotaFileEntry) => number | null
): QuotaFileEntry[] {
  if (mode !== 'soonest') return [...entries];

  // Decorate once — resolving pokes at provider-shaped state per entry.
  return entries
    .map((entry, index) => ({ entry, index, atMs: resolveNextRecoveryMs(entry) }))
    .sort((a, b) => {
      if (a.atMs === null && b.atMs === null) return a.index - b.index;
      if (a.atMs === null) return 1;
      if (b.atMs === null) return -1;
      return a.atMs - b.atMs || a.index - b.index;
    })
    .map((decorated) => decorated.entry);
}

export function buildTabCounts(entries: QuotaFileEntry[]): Record<string, number> {
  const counts: Record<string, number> = { all: entries.length };
  for (const type of QUOTA_TAB_ORDER) {
    counts[type] = 0;
  }
  for (const entry of entries) {
    counts[entry.type] += 1;
  }
  return counts;
}

export const isQuotaRefreshDisabled = (
  canRefresh: boolean,
  loading: boolean,
  resetting: boolean
): boolean => !canRefresh || loading || resetting;

export type QuotaDisplayParts = {
  /** Pinned channel prefix, or the complete unpinned display name. */
  primary: string;
  /** Remaining pinned filename, including .json; otherwise null. */
  secondary: string | null;
  /** Full display name, including Devin identity disambiguation. */
  full: string;
};

/** Match credential-card naming and retain Devin identity disambiguation. */
export const getQuotaDisplayParts = (file: AuthFileItem): QuotaDisplayParts => {
  const full = getQuotaDisplayName(file);
  const pinned = derivePinnedFileName(file);
  if (!pinned) return { primary: full, secondary: null, full };
  const suffix = full.startsWith(file.name) ? full.slice(file.name.length) : '';
  return { primary: pinned.title + suffix, secondary: pinned.subtitle, full };
};
