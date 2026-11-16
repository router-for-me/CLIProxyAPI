import type { QuotaProviderType } from './providers/types';

/** Tab order, which is also the provider grouping order of the cards under the 'All' tab. */
export const QUOTA_TAB_ORDER: readonly QuotaProviderType[] = [
  'claude',
  'antigravity',
  'codex',
  'xai',
  'kimi',
  'devin',
  'meta',
];

export type QuotaTabId = 'all' | QuotaProviderType;

/** Fixed page size of 20, which also caps the upstream concurrency of "Refresh all" at 20. */
export const QUOTA_PAGE_SIZE = 20;

/** Card sort: default = provider grouping order; soonest = soonest recovery first. */
export const QUOTA_SORT_MODES = ['default', 'soonest'] as const;

export type QuotaSortMode = (typeof QUOTA_SORT_MODES)[number];

/** Matches useRevealGroup's GROUP_MAX_TOTAL: the card cascade's total budget is 360ms. */
export const CARD_ENTRANCE_BUDGET_MS = 360;
