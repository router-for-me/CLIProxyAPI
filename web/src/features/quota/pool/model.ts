import { burnRate, drainProjected, drainRecorded } from '@/features/quota/pool/burn';
import { poolRange } from '@/features/quota/pool/calendar';
import {
  ACCOUNT_CAPACITY,
  EPS,
  FUTURE_FACTOR,
  PROJECTION_HORIZON_MS,
} from '@/features/quota/pool/constants';
import { niceCeil } from '@/features/quota/pool/geometry';
import { buildPoolMembers } from '@/features/quota/pool/members';
import { projectPool } from '@/features/quota/pool/projection';
import { recordedPool } from '@/features/quota/pool/recorded';
import { remainingAt } from '@/features/quota/pool/track';
import type {
  FirstEmptyStat,
  HistoryStatus,
  NextResetStat,
  PoolCardModel,
  PoolEmptyStat,
  PoolMember,
  PoolModelInput,
  Projection,
  RecordedSegment,
  XY,
} from '@/features/quota/pool/types';
import { HOUR_MS, MINUTE_MS } from '@/utils/time/durations';

type NoticeStatus = Exclude<HistoryStatus, 'ok' | 'loading'>;

const NOTICE_SEVERITY: readonly NoticeStatus[] = ['error', 'unsupported', 'disabled', 'empty'];

/**
 * Projected event instants shown as clock text, rounded once to the nearest
 * minute so every label and countdown names the same minute.
 */
const toEventMinute = (ms: number): number => Math.round(ms / MINUTE_MS) * MINUTE_MS;

const getPoolEmptyStat = (
  projection: Projection | null,
  poolNow: number,
  now: number,
  horizonEnd: number
): PoolEmptyStat => {
  if (projection === null) return { kind: 'unknown' };

  const { poolEmptyAt } = projection;

  if (poolNow <= EPS || (poolEmptyAt !== null && poolEmptyAt <= now)) return { kind: 'now' };
  if (poolEmptyAt !== null && poolEmptyAt <= horizonEnd)
    return { at: toEventMinute(poolEmptyAt), kind: 'at' };

  return { horizonEnd, kind: 'never' };
};

const getFirstEmptyStat = (
  projection: Projection | null,
  now: number,
  horizonEnd: number
): FirstEmptyStat => {
  if (projection === null) return { kind: 'unknown' };

  const { firstEmpty } = projection;

  if (firstEmpty === null || firstEmpty.at > horizonEnd) return { horizonEnd, kind: 'never' };
  if (firstEmpty.at <= now) return { key: firstEmpty.key, kind: 'now' };

  return { at: toEventMinute(firstEmpty.at), key: firstEmpty.key, kind: 'at' };
};

const getNextResetStat = (
  projection: Projection | null,
  lowerBound: Projection | null,
  now: number,
  horizonEnd: number
): NextResetStat => {
  const step = (projection ?? lowerBound)?.steps.find(
    ({ gain, t }) => t > now && t <= horizonEnd && Math.round(gain) >= 1
  );

  if (!step) return { kind: 'none' };

  return { at: step.t, gain: Math.round(step.gain), kind: 'at', lowerBound: projection === null };
};

const getHistoryStatus = (
  history: PoolModelInput['history'],
  members: PoolMember[]
): HistoryStatus => {
  if (history === null) return 'loading';

  const hasHistory = members.some((member) => member.readings.some((reading) => !reading.live));

  return history.status === 'ok' && !hasHistory ? 'empty' : history.status;
};

const drainYMax = (recorded: RecordedSegment[], projected: XY[] | null, rate: number | null) => {
  const values = [
    ...recorded.flatMap((segment) => segment.points.map((point) => point.v)),
    ...(projected ?? []).map((point) => point.v),
    ...(rate === null ? [] : [rate]),
  ];

  return niceCeil(1.1 * Math.max(0, ...values));
};

const getProjectionRate = (
  burnValue: number | null,
  futureUsage: PoolModelInput['futureUsage']
): number | null => {
  if (futureUsage === 'none') return 0;

  return burnValue === null ? null : burnValue * FUTURE_FACTOR[futureUsage];
};

/** One pool card's numbers; null when no file has a weekly reading. */
export const buildPoolCardModel = (input: PoolModelInput): PoolCardModel | null => {
  const { burnWindow, dataAt, futureUsage, history, metric, now, provider } = input;
  const { members, noDataCount } = buildPoolMembers(input);

  if (members.length === 0) return null;

  const range = poolRange(now, input.rangeOffset, input.timeZone);
  const horizonEnd = now + PROJECTION_HORIZON_MS;
  const simulationEnd = Math.max(horizonEnd, range.end);
  const windowMs = burnWindow * HOUR_MS;
  const recordedEnd = Math.min(dataAt, range.end);
  const capacity = members.length * ACCOUNT_CAPACITY;
  const poolNow = members.reduce((sum, member) => sum + (remainingAt(member, dataAt) ?? 0), 0);
  const burn = burnRate(members, dataAt, windowMs);
  const rate = getProjectionRate(burn.value, futureUsage);
  const projection = rate === null ? null : projectPool(members, dataAt, rate, simulationEnd);
  const lowerBound = projection ? null : projectPool(members, dataAt, 0, simulationEnd);
  const recorded = recordedPool(members, range.start, recordedEnd);
  const projectedSteps = (projection?.steps ?? []).filter(
    ({ gain, t }) => Math.round(gain) >= 1 && t >= range.start && t <= range.end
  );
  const emptyAt = projection?.poolEmptyAt == null ? null : toEventMinute(projection.poolEmptyAt);
  const drainRecordedSegments =
    metric === 'drain' ? drainRecorded(members, range.start, recordedEnd, windowMs) : [];
  const drainProjectedPoints = projection ? drainProjected(projection) : null;

  return {
    burn: { ...burn, hours: burnWindow },
    capacity,
    dataAt,
    drain: {
      projected: drainProjectedPoints,
      recorded: drainRecordedSegments,
      yMax: drainYMax(drainRecordedSegments, drainProjectedPoints, projection?.rate ?? null),
    },
    historyStatus: getHistoryStatus(history, members),
    lowerBound,
    memberCount: members.length,
    members,
    noDataCount,
    now,
    poolNow,
    projection,
    provider,
    range,
    remaining: {
      emptyAt: emptyAt !== null && emptyAt >= range.start && emptyAt <= range.end ? emptyAt : null,
      projected: projection?.points ?? null,
      recorded: recorded.segments,
      steps: [...recorded.steps, ...projectedSteps],
      yMax: capacity,
    },
    stats: {
      firstEmpty: getFirstEmptyStat(projection, now, horizonEnd),
      nextReset: getNextResetStat(projection, lowerBound, now, horizonEnd),
      poolEmpty: getPoolEmptyStat(projection, poolNow, now, horizonEnd),
    },
  };
};

/** The single notice status across cards: error > unsupported > disabled > empty. */
export const worstHistoryStatus = (models: PoolCardModel[]): NoticeStatus | null =>
  NOTICE_SEVERITY.find((status) => models.some((model) => model.historyStatus === status)) ?? null;
