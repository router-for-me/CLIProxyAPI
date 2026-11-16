import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/utils/time/durations';

export const POOL_PROVIDERS = ['claude', 'codex'] as const;
export type PoolProvider = (typeof POOL_PROVIDERS)[number];

export const POOL_METRICS = ['remaining', 'drain'] as const;
export type PoolMetric = (typeof POOL_METRICS)[number];

export const BURN_WINDOWS = [1, 3, 6] as const;
export type BurnWindowHours = (typeof BURN_WINDOWS)[number];

export const FUTURE_USAGES = ['same', 'half', 'double', 'none'] as const;
export type FutureUsage = (typeof FUTURE_USAGES)[number];

export const FUTURE_FACTOR: Record<FutureUsage, number> = {
  double: 2,
  half: 0.5,
  none: 0,
  same: 1,
};

/** Every account contributes 100% to its pool. */
export const ACCOUNT_CAPACITY = 100;
export const DEFAULT_PERIOD_MS = 168 * HOUR_MS;
export const PROJECTION_HORIZON_MS = 14 * DAY_MS;
export const RESET_MERGE_MS = 60_000;
export const MIN_BURN_COVERAGE_MS = 15 * MINUTE_MS;
/** Readings of one member further apart than this are an observation gap. */
export const STALE_GAP_MS = 4 * HOUR_MS;
/** Recorded-line downsampling for drawing (min/max per pixel column). */
export const MAX_DRAW_POINTS_PER_PX = 2;
export const DRAIN_GRID_MS = 15 * MINUTE_MS;
export const RANGE_DAYS = 14;
export const RANGE_LEAD_DAYS = 3;
export const RANGE_STEP_DAYS = 7;
export const RANGE_MIN_OFFSET = -3;
export const RANGE_MAX_OFFSET = 1;
export const MAX_SIM_EVENTS = 10_000;
export const EPS = 1e-9;
export const HISTORY_RETENTION_HOURS_DEFAULT = 720;
