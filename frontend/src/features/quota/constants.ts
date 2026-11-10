import type { QuotaProviderType } from './providers/types';

/** Provider grouping is shared by tabs and the all-provider grid. */
export const QUOTA_TAB_ORDER: readonly QuotaProviderType[] = [
  'claude',
  'antigravity',
  'codex',
  'xai',
  'kiro',
  'kimi',
  'qoder',
  'zcode',
  'devin',
  'meta',
];

export type QuotaTabId = 'all' | QuotaProviderType;

/** Pagination and upstream refresh concurrency share the same bound. */
export const QUOTA_PAGE_SIZE = 20;

export const QUOTA_SORT_MODES = ['default', 'soonest'] as const;
export type QuotaSortMode = (typeof QUOTA_SORT_MODES)[number];

/** Matches the card-level useRevealGroup entrance budget. */
export const CARD_ENTRANCE_BUDGET_MS = 360;
