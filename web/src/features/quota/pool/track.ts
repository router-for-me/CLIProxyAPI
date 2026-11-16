import { ACCOUNT_CAPACITY, STALE_GAP_MS } from '@/features/quota/pool/constants';
import type { PoolMember, Reading } from '@/features/quota/pool/types';

/** Index of the last reading with r.t <= t, or -1. Binary search. */
export const readingIndexAt = (member: PoolMember, t: number): number => {
  const { readings } = member;
  let low = 0;
  let high = readings.length;

  while (low < high) {
    const middle = (low + high) >>> 1;

    if (readings[middle].t <= t) {
      low = middle + 1;
    } else {
      high = middle;
    }
  }

  return low - 1;
};

/** readingIndexAt for a later t, walking forward from a previous answer. */
export const advanceReadingIndex = (member: PoolMember, index: number, t: number): number => {
  const { readings } = member;
  let next = index;

  while (next + 1 < readings.length && readings[next + 1].t <= t) next += 1;

  return next;
};

const fractionOf = (reading: Reading, t: number): number =>
  reading.resetAt !== null && t >= reading.resetAt ? 1 : 1 - reading.used;

/** remainingAt when the active reading index at t is already known. */
export const remainingAtIndex = (member: PoolMember, index: number, t: number): number | null =>
  index < 0 ? null : ACCOUNT_CAPACITY * fractionOf(member.readings[index], t);

/** Carry-forward of the last reading, full from its resetAt on (Go `current()`); null before the first reading. */
export const remainingFraction = (member: PoolMember, t: number): number | null => {
  const index = readingIndexAt(member, t);

  return index < 0 ? null : fractionOf(member.readings[index], t);
};

export const remainingAt = (member: PoolMember, t: number): number | null =>
  remainingAtIndex(member, readingIndexAt(member, t), t);

/** True when the interval starting at readings[index] is an observation gap. */
export const isStaleAfter = (member: PoolMember, index: number): boolean => {
  const { readings } = member;

  return (
    index >= 0 &&
    index + 1 < readings.length &&
    readings[index + 1].t - readings[index].t > STALE_GAP_MS
  );
};

/**
 * Reading times and reset instants in (from, to], sorted and distinct. Only
 * readings active somewhere in [from, to] can change the track there.
 */
export const memberBreakpoints = (member: PoolMember, from: number, to: number): number[] => {
  const { readings } = member;
  const breakpoints = new Set<number>();
  const isInside = (t: number) => t > from && t <= to;
  let index = Math.max(0, readingIndexAt(member, from));

  while (index < readings.length && readings[index].t <= to) {
    const { resetAt, t } = readings[index];

    if (isInside(t)) breakpoints.add(t);
    if (resetAt !== null && isInside(resetAt)) breakpoints.add(resetAt);

    index += 1;
  }

  return [...breakpoints].sort((left, right) => left - right);
};

const stepDeclines = (member: PoolMember, from: number, to: number): number => {
  const times = memberBreakpoints(member, from, to);
  let index = readingIndexAt(member, from);
  let previous = remainingAtIndex(member, index, from);
  let previousIndex = index;
  let total = 0;

  if (times[times.length - 1] !== to) times.push(to);

  for (const t of times) {
    index = advanceReadingIndex(member, index, t);

    const value = remainingAtIndex(member, index, t);
    const isInsideGap =
      isStaleAfter(member, previousIndex) && t <= member.readings[previousIndex + 1].t;

    if (previous !== null && value !== null && !isInsideGap) {
      total += Math.max(0, previous - value);
    }

    previous = value;
    previousIndex = index;
  }

  return total;
};

const spreadShare = (member: PoolMember, index: number, from: number, to: number): number => {
  const before = member.readings[index - 1];
  const after = member.readings[index];
  const refillAt =
    before.resetAt !== null && before.resetAt > before.t && before.resetAt <= after.t
      ? before.resetAt
      : null;
  const start = refillAt === null ? ACCOUNT_CAPACITY * (1 - before.used) : ACCOUNT_CAPACITY;
  const spanStart = refillAt ?? before.t;
  const span = after.t - spanStart;
  const overlap = Math.min(to, after.t) - Math.max(from, spanStart);
  const decline = Math.max(0, start - ACCOUNT_CAPACITY * fractionOf(after, after.t));

  return span > 0 && overlap > 0 ? (decline * overlap) / span : 0;
};

/**
 * Weighted points the member lost in [from, to]. Declines are attributed at the
 * reading that shows them, except across an observation gap, where the decline
 * is spread evenly over the gap. Reset jumps are increases and never count.
 */
export const declinesBetween = (
  member: PoolMember,
  from: number,
  to: number
): { points: number; spread: boolean } => {
  const { readings } = member;
  let points = stepDeclines(member, from, to);
  let spread = false;
  let index = Math.max(1, readingIndexAt(member, from) + 1);

  while (index < readings.length && readings[index - 1].t < to) {
    if (isStaleAfter(member, index - 1)) {
      const share = spreadShare(member, index, from, to);

      points += share;
      spread ||= share > 0;
    }

    index += 1;
  }

  return { points, spread };
};
