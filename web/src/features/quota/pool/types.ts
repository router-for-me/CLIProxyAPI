import type {
  BurnWindowHours,
  FutureUsage,
  PoolMetric,
  PoolProvider,
} from '@/features/quota/pool/constants';
import type { QuotaHistoryResult } from '@/services/api/quotaHistory';
import type { AuthFileItem } from '@/types';

export interface Reading {
  live: boolean;
  resetAt: number | null;
  t: number;
  used: number;
}

export interface PoolMember {
  file: AuthFileItem;
  /** getQuotaCacheKey(file). */
  key: string;
  ordinal: number;
  periodMs: number;
  /** Ascending, distinct t: history (live=false, t < dataAt), then the live reading at dataAt. */
  readings: Reading[];
}

/** Equal consecutive t is a vertical riser. */
export interface XY {
  t: number;
  v: number;
}

export interface RecordedSegment {
  /** Members covered throughout the segment; a change in coverage starts a new segment. */
  known: number;
  partial: boolean;
  points: XY[];
  /** Some member's reading spacing exceeds STALE_GAP_MS here; partial is then true too. */
  stale: boolean;
}

export interface StepMark {
  gain: number;
  keys: string[];
  kind: 'recorded' | 'projected';
  t: number;
}

export interface BurnRate {
  approximate: boolean;
  coveredMs: number;
  value: number | null;
}

export interface Projection {
  end: number;
  firstEmpty: { at: number; key: string } | null;
  points: XY[];
  poolEmptyAt: number | null;
  rate: number;
  steps: StepMark[];
}

export type PoolEmptyStat =
  | { at: number; kind: 'at' }
  | { kind: 'now' }
  | { horizonEnd: number; kind: 'never' }
  | { kind: 'unknown' };

export type FirstEmptyStat =
  | { at: number; key: string; kind: 'at' }
  | { key: string; kind: 'now' }
  | { horizonEnd: number; kind: 'never' }
  | { kind: 'unknown' };

export type NextResetStat =
  { at: number; gain: number; kind: 'at'; lowerBound: boolean } | { kind: 'none' };

export interface DayCell {
  end: number;
  start: number;
}

export interface PoolRange {
  canNext: boolean;
  canPrev: boolean;
  days: DayCell[];
  end: number;
  isToday: boolean;
  offset: number;
  start: number;
}

export type HistoryStatus = 'loading' | 'ok' | 'empty' | 'unsupported' | 'disabled' | 'error';

export interface PoolCardModel {
  burn: BurnRate & { hours: BurnWindowHours };
  capacity: number;
  dataAt: number;
  drain: { projected: XY[] | null; recorded: RecordedSegment[]; yMax: number };
  historyStatus: HistoryStatus;
  /** Set only when projection is null: the rate-0 run. */
  lowerBound: Projection | null;
  memberCount: number;
  members: PoolMember[];
  noDataCount: number;
  now: number;
  poolNow: number;
  /** Null when burn.value is null and futureUsage is not 'none'. */
  projection: Projection | null;
  provider: PoolProvider;
  range: PoolRange;
  remaining: {
    emptyAt: number | null;
    projected: XY[] | null;
    recorded: RecordedSegment[];
    steps: StepMark[];
    yMax: number;
  };
  stats: { firstEmpty: FirstEmptyStat; nextReset: NextResetStat; poolEmpty: PoolEmptyStat };
}

export interface PoolModelInput {
  burnWindow: BurnWindowHours;
  /** When the files list was last loaded, <= now: live reading, burn, poolNow, projection start. */
  dataAt: number;
  /** Classified, enabled, this provider; not search-filtered, not paginated. */
  files: AuthFileItem[];
  futureUsage: FutureUsage;
  /** Null while loading; point times already on the browser clock. */
  history: QuotaHistoryResult | null;
  /** drain.recorded is computed only for 'drain'. */
  metric: PoolMetric;
  /** Wall clock: Now marker, range, horizon, countdowns. */
  now: number;
  /** accountOrdinals() over the same provider's files. */
  ordinals: Map<string, number>;
  provider: PoolProvider;
  rangeOffset: number;
  timeZone: string;
}
