import { RESET_MERGE_MS } from '@/features/quota/pool/constants';
import {
  advanceReadingIndex,
  isStaleAfter,
  memberBreakpoints,
  readingIndexAt,
  remainingAt,
  remainingAtIndex,
} from '@/features/quota/pool/track';
import type { PoolMember, RecordedSegment, StepMark } from '@/features/quota/pool/types';

interface PoolSample {
  known: number;
  partial: boolean;
  stale: boolean;
  t: number;
  v: number;
}

interface ResetInstant {
  member: PoolMember;
  t: number;
}

/** Breakpoints of every member in (from, to], plus from and to. Stale-gap ends are reading times, so they are included. */
const poolBreakpoints = (members: PoolMember[], from: number, to: number): number[] => {
  const times = new Set<number>([from, to]);

  for (const member of members) {
    for (const t of memberBreakpoints(member, from, to)) times.add(t);
  }

  return [...times].sort((left, right) => left - right);
};

const samplePool = (members: PoolMember[], times: number[]): Array<PoolSample | null> => {
  const indexes = members.map((member) => readingIndexAt(member, times[0]));

  return times.map((t) => {
    let known = 0;
    let stale = false;
    let v = 0;

    for (const [position, member] of members.entries()) {
      const index = advanceReadingIndex(member, indexes[position], t);
      const value = remainingAtIndex(member, index, t);

      indexes[position] = index;

      if (value !== null) {
        known += 1;
        v += value;
        stale ||= isStaleAfter(member, index);
      }
    }

    if (known === 0) return null;

    return { known, partial: known < members.length || stale, stale, t, v };
  });
};

const hasTwoDistinctTimes = (segment: RecordedSegment): boolean =>
  segment.points.some((point) => point.t !== segment.points[0].t);

const startSegment = ({ known, partial, stale, t, v }: PoolSample): RecordedSegment => ({
  known,
  partial,
  points: [{ t, v }],
  stale,
});

/**
 * Step-after series of the samples, split at gaps and wherever `partial` or
 * the covered member count changes. A riser belongs to the segment it ends; the next segment starts at
 * its top, so the line stays continuous.
 */
const buildSegments = (samples: Array<PoolSample | null>): RecordedSegment[] => {
  const segments: RecordedSegment[] = [];
  let current: RecordedSegment | null = null;

  for (const sample of samples) {
    if (sample === null) {
      current = null;
      continue;
    }

    if (current === null) {
      current = startSegment(sample);
      segments.push(current);
      continue;
    }

    const previousV = current.points[current.points.length - 1].v;

    if (previousV !== sample.v) current.points.push({ t: sample.t, v: previousV });
    current.points.push({ t: sample.t, v: sample.v });

    if (sample.partial === current.partial && sample.known === current.known) {
      current.stale ||= sample.stale;
      continue;
    }

    current = startSegment(sample);
    segments.push(current);
  }

  return segments.filter(hasTwoDistinctTimes);
};

const findResetInstants = (members: PoolMember[], from: number, to: number): ResetInstant[] => {
  const instants: ResetInstant[] = [];

  for (const member of members) {
    const { readings } = member;
    let index = Math.max(0, readingIndexAt(member, from));

    while (index < readings.length && readings[index].t < to) {
      const { resetAt, t } = readings[index];
      const nextT = readings[index + 1]?.t ?? Infinity;
      const isActiveAtReset = resetAt !== null && t < resetAt && nextT >= resetAt;

      if (isActiveAtReset && resetAt > from && resetAt <= to) {
        instants.push({ member, t: resetAt });
      }

      index += 1;
    }
  }

  return instants.sort((left, right) => left.t - right.t);
};

const groupInstants = (instants: ResetInstant[]): ResetInstant[][] => {
  const groups: ResetInstant[][] = [];

  for (const instant of instants) {
    const group = groups[groups.length - 1];

    if (group && instant.t - group[0].t < RESET_MERGE_MS) {
      group.push(instant);
    } else {
      groups.push([instant]);
    }
  }

  return groups;
};

const refillOf = ({ member, t }: ResetInstant): number =>
  (remainingAt(member, t) ?? 0) - (remainingAt(member, t - 1) ?? 0);

/** Recorded reset steps; each member is measured at its own reset instant. */
const recordedSteps = (members: PoolMember[], from: number, to: number): StepMark[] =>
  groupInstants(findResetInstants(members, from, to))
    .map((group) => ({
      gain: group.reduce((sum, instant) => sum + refillOf(instant), 0),
      keys: group.map((instant) => instant.member.key),
      kind: 'recorded' as const,
      t: group[0].t,
    }))
    .filter((step) => Math.round(step.gain) >= 1);

/** The recorded pool over [from, to]: carry-forward plus the reset rule, partial coverage marked. */
export const recordedPool = (
  members: PoolMember[],
  from: number,
  to: number
): { segments: RecordedSegment[]; steps: StepMark[] } => {
  if (to <= from) return { segments: [], steps: [] };

  const samples = samplePool(members, poolBreakpoints(members, from, to));

  return { segments: buildSegments(samples), steps: recordedSteps(members, from, to) };
};
