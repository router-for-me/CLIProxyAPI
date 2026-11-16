import { DRAIN_GRID_MS, MIN_BURN_COVERAGE_MS } from '@/features/quota/pool/constants';
import { declinesBetween } from '@/features/quota/pool/track';
import type {
  BurnRate,
  PoolMember,
  Projection,
  RecordedSegment,
  XY,
} from '@/features/quota/pool/types';
import { HOUR_MS } from '@/utils/time/durations';

const firstHistoryTime = (members: PoolMember[]): number =>
  members.reduce((earliest, { readings }) => {
    const first = readings[0];

    return first && !first.live ? Math.min(earliest, first.t) : earliest;
  }, Infinity);

/** Weighted points per hour lost over the trailing window, divided by the hours history covers. */
export const burnRate = (members: PoolMember[], at: number, windowMs: number): BurnRate => {
  const firstHistory = firstHistoryTime(members);

  if (firstHistory === Infinity) return { approximate: false, coveredMs: 0, value: null };

  const coveredFrom = Math.max(at - windowMs, firstHistory);
  const coveredMs = Math.max(0, at - coveredFrom);

  if (coveredMs < MIN_BURN_COVERAGE_MS) return { approximate: false, coveredMs, value: null };

  let points = 0;
  let spread = false;

  for (const member of members) {
    const declines = declinesBetween(member, coveredFrom, at);

    points += declines.points;
    spread ||= declines.spread;
  }

  return {
    approximate: coveredMs < windowMs || spread,
    coveredMs,
    value: points / (coveredMs / HOUR_MS),
  };
};

const drainGrid = (from: number, to: number): number[] => {
  const times: number[] = [];
  let t = Math.ceil(from / DRAIN_GRID_MS) * DRAIN_GRID_MS;

  while (t <= to) {
    times.push(t);
    t += DRAIN_GRID_MS;
  }

  if (times[times.length - 1] !== to) times.push(to);

  return times;
};

/** The recorded burn rate as a linear polyline on a 15-minute grid; unknown rates are gaps. */
export const drainRecorded = (
  members: PoolMember[],
  from: number,
  to: number,
  windowMs: number
): RecordedSegment[] => {
  if (to <= from) return [];

  const segments: RecordedSegment[] = [];
  let current: XY[] | null = null;

  for (const t of drainGrid(from, to)) {
    const { value } = burnRate(members, t, windowMs);

    if (value === null) {
      current = null;
    } else if (current === null) {
      current = [{ t, v: value }];
      segments.push({ known: members.length, partial: false, points: current, stale: false });
    } else {
      current.push({ t, v: value });
    }
  }

  return segments.filter((segment) => segment.points.length > 1);
};

/** The projected burn per stretch between events; risers are skipped. */
export const drainProjected = (projection: Projection): XY[] =>
  projection.points.slice(1).flatMap((end, index) => {
    const start = projection.points[index];

    if (end.t <= start.t) return [];

    const v = Math.max(0, start.v - end.v) / ((end.t - start.t) / HOUR_MS);

    return [
      { t: start.t, v },
      { t: end.t, v },
    ];
  });
