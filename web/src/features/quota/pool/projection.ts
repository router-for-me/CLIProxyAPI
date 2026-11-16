import {
  ACCOUNT_CAPACITY,
  EPS,
  MAX_SIM_EVENTS,
  RESET_MERGE_MS,
} from '@/features/quota/pool/constants';
import { readingIndexAt, remainingAt } from '@/features/quota/pool/track';
import type { PoolMember, Projection, StepMark, XY } from '@/features/quota/pool/types';
import { HOUR_MS } from '@/utils/time/durations';

interface DrainState {
  key: string;
  next: number | null;
  periodMs: number;
  remaining: number;
}

const nextResetAfter = (member: PoolMember, start: number): number | null => {
  const index = readingIndexAt(member, start);
  const resetAt = index < 0 ? null : member.readings[index].resetAt;

  if (resetAt === null || resetAt > start) return resetAt;

  return resetAt + (Math.floor((start - resetAt) / member.periodMs) + 1) * member.periodMs;
};

const toDrainState = (member: PoolMember, start: number): DrainState => ({
  key: member.key,
  next: nextResetAfter(member, start),
  periodMs: member.periodMs,
  remaining: remainingAt(member, start) ?? 0,
});

/** EarliestResetSelector order: earliest reset, then most remaining, then key. */
const compareDrainOrder = (left: DrainState, right: DrainState): number => {
  const leftNext = left.next ?? Infinity;
  const rightNext = right.next ?? Infinity;

  if (leftNext !== rightNext) return leftNext < rightNext ? -1 : 1;

  if (left.remaining !== right.remaining) return right.remaining - left.remaining;
  if (left.key === right.key) return 0;

  return left.key < right.key ? -1 : 1;
};

const sumRemaining = (states: DrainState[]): number =>
  states.reduce((sum, state) => sum + state.remaining, 0);

const earliestReset = (states: DrainState[]): number =>
  states.reduce((earliest, state) => Math.min(earliest, state.next ?? Infinity), Infinity);

const applyResets = (states: DrainState[], resetAt: number): StepMark => {
  const group = states.filter(
    (state): state is DrainState & { next: number } =>
      state.next !== null && state.next < resetAt + RESET_MERGE_MS
  );
  let gain = 0;

  for (const state of group) {
    gain += ACCOUNT_CAPACITY - state.remaining;
    state.remaining = ACCOUNT_CAPACITY;
    state.next += state.periodMs;
  }

  return { gain, keys: group.map((state) => state.key), kind: 'projected', t: resetAt };
};

/**
 * Event simulation from `start`: the pool falls linearly at `ratePerHour`,
 * draining one member at a time in reset order, and members refill at their
 * resets. Resets less than RESET_MERGE_MS apart form one step.
 */
export const projectPool = (
  members: PoolMember[],
  start: number,
  ratePerHour: number,
  end: number
): Projection => {
  const states = members.map((member) => toDrainState(member, start));
  const initialPool = sumRemaining(states);
  const points: XY[] = [{ t: start, v: initialPool }];
  const steps: StepMark[] = [];
  const emptyAtStart = [...states].sort(compareDrainOrder).find((state) => state.remaining <= EPS);
  let firstEmpty = emptyAtStart ? { at: start, key: emptyAtStart.key } : null;
  let poolEmptyAt = initialPool <= EPS ? start : null;
  let t = start;
  let events = 0;

  while (events < MAX_SIM_EVENTS && t < end) {
    const active = [...states].sort(compareDrainOrder).find((state) => state.remaining > EPS);
    const draining = active && ratePerHour > 0 ? active : null;
    const emptiesAt = draining ? t + (draining.remaining / ratePerHour) * HOUR_MS : Infinity;
    const resetAt = earliestReset(states);
    const nextT = Math.min(emptiesAt, resetAt, end);

    if (draining) {
      const drained = (ratePerHour * (nextT - t)) / HOUR_MS;

      draining.remaining = nextT === emptiesAt ? 0 : Math.max(0, draining.remaining - drained);
    }

    t = nextT;

    const pool = sumRemaining(states);

    points.push({ t, v: pool });

    if (draining && t === emptiesAt) {
      // A member that refills within RESET_MERGE_MS is not empty, as for the pool below.
      const refillsAtOnce = draining.next !== null && draining.next < t + RESET_MERGE_MS;

      if (!refillsAtOnce) firstEmpty ??= { at: t, key: draining.key };

      if (pool <= EPS && !(resetAt < t + RESET_MERGE_MS)) poolEmptyAt ??= t;
    }

    if (resetAt !== Infinity && t === resetAt) {
      const step = applyResets(states, resetAt);

      steps.push(step);
      points.push({ t, v: pool + step.gain });
    }

    events += 1;
  }

  return { end, firstEmpty, points, poolEmptyAt, rate: ratePerHour, steps };
};
