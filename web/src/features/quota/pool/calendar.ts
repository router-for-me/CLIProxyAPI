import {
  RANGE_DAYS,
  RANGE_LEAD_DAYS,
  RANGE_MAX_OFFSET,
  RANGE_MIN_OFFSET,
  RANGE_STEP_DAYS,
} from '@/features/quota/pool/constants';
import type { DayCell, PoolRange } from '@/features/quota/pool/types';
import { DAY_MS, HOUR_MS } from '@/utils/time/durations';

interface LocalParts {
  day: number;
  hour: number;
  minute: number;
  month: number;
  second: number;
  year: number;
}

const partFormatters = new Map<string, Intl.DateTimeFormat>();

const getPartFormatter = (timeZone: string): Intl.DateTimeFormat => {
  const cached = partFormatters.get(timeZone);

  if (cached) return cached;

  const formatter = new Intl.DateTimeFormat('en-US', {
    day: 'numeric',
    hour: 'numeric',
    hourCycle: 'h23',
    minute: 'numeric',
    month: 'numeric',
    second: 'numeric',
    timeZone,
    year: 'numeric',
  });

  partFormatters.set(timeZone, formatter);

  return formatter;
};

const getLocalParts = (ms: number, timeZone: string): LocalParts => {
  const parts: LocalParts = { day: 0, hour: 0, minute: 0, month: 0, second: 0, year: 0 };

  for (const { type, value } of getPartFormatter(timeZone).formatToParts(ms)) {
    if (type in parts) parts[type as keyof LocalParts] = Number(value);
  }

  return parts;
};

/** Local time minus UTC at `ms`, in ms. */
export const zonedOffsetMs = (ms: number, timeZone: string): number => {
  const { day, hour, minute, month, second, year } = getLocalParts(ms, timeZone);
  const wholeSecond = Math.floor(ms / 1000) * 1000;

  return Date.UTC(year, month - 1, day, hour, minute, second) - wholeSecond;
};

const isSameLocalDate = (ms: number, localMidnightAsUtc: number, timeZone: string): boolean => {
  const { day, month, year } = getLocalParts(ms, timeZone);
  const target = new Date(localMidnightAsUtc);

  return (
    year === target.getUTCFullYear() &&
    month === target.getUTCMonth() + 1 &&
    day === target.getUTCDate()
  );
};

/**
 * The first instant of the local date `localMidnightAsUtc` names. Tries the
 * offsets of the day before and the day after, so a DST gap at midnight starts
 * the day at the transition instead of an hour early on the previous date.
 */
const toZonedInstant = (localMidnightAsUtc: number, timeZone: string): number => {
  const candidates = [
    localMidnightAsUtc - zonedOffsetMs(localMidnightAsUtc - DAY_MS, timeZone),
    localMidnightAsUtc - zonedOffsetMs(localMidnightAsUtc + DAY_MS, timeZone),
  ].sort((left, right) => left - right);
  const match = candidates.find((candidate) =>
    isSameLocalDate(candidate, localMidnightAsUtc, timeZone)
  );

  if (match !== undefined) return match;

  const firstGuess = localMidnightAsUtc - zonedOffsetMs(localMidnightAsUtc, timeZone);

  return localMidnightAsUtc - zonedOffsetMs(firstGuess, timeZone);
};

/** Start of the local day containing `ms`. */
export const zonedMidnight = (ms: number, timeZone: string): number => {
  const { day, month, year } = getLocalParts(ms, timeZone);

  return toZonedInstant(Date.UTC(year, month - 1, day), timeZone);
};

/** The local midnight `days` calendar days after `midnight`. */
export const addZonedDays = (midnight: number, days: number, timeZone: string): number => {
  const { day, month, year } = getLocalParts(midnight, timeZone);

  return toZonedInstant(Date.UTC(year, month - 1, day + days), timeZone);
};

const clampOffset = (offset: number): number =>
  Math.min(RANGE_MAX_OFFSET, Math.max(RANGE_MIN_OFFSET, offset));

/** Fourteen local days starting at midnight RANGE_LEAD_DAYS before today, moved by whole weeks. */
export const poolRange = (now: number, offset: number, timeZone: string): PoolRange => {
  const clamped = clampOffset(offset);
  const start = addZonedDays(
    zonedMidnight(now, timeZone),
    RANGE_STEP_DAYS * clamped - RANGE_LEAD_DAYS,
    timeZone
  );
  const boundaries = Array.from({ length: RANGE_DAYS + 1 }, (_, day) =>
    addZonedDays(start, day, timeZone)
  );
  const days: DayCell[] = boundaries
    .slice(0, RANGE_DAYS)
    .map((dayStart, day) => ({ end: boundaries[day + 1], start: dayStart }));

  return {
    canNext: clamped < RANGE_MAX_OFFSET,
    canPrev: clamped > RANGE_MIN_OFFSET,
    days,
    end: boundaries[RANGE_DAYS],
    isToday: clamped === 0,
    offset: clamped,
    start,
  };
};

/** The history fetch start: the range start, or 7h before now when that is earlier, floored to the hour. */
export const historySinceMs = (range: PoolRange, now: number): number =>
  Math.floor(Math.min(range.start, now - 7 * HOUR_MS) / HOUR_MS) * HOUR_MS;
