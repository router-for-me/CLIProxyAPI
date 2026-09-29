/**
 * Shared minute clock: snapshot stability and timer lifecycle.
 *
 * The stability case is the important one — a `getSnapshot` that returns a
 * fresh `Date.now()` makes React's useSyncExternalStore re-render forever.
 */

import { describe, expect, test } from 'bun:test';
import { createSharedClock } from '@/utils/time/sharedClock';

/** Deterministic stand-in for setInterval: fires only when the test says so. */
function makeFakeTimers() {
  const timers = new Map<number, () => void>();
  let nextId = 1;
  let created = 0;
  let cleared = 0;

  return {
    created: () => created,
    cleared: () => cleared,
    active: () => timers.size,
    fireAll: () => timers.forEach((fn) => fn()),
    setTimer: (fn: () => void) => {
      created += 1;
      const id = nextId++;
      timers.set(id, fn);
      return id;
    },
    clearTimer: (id: unknown) => {
      cleared += 1;
      timers.delete(id as number);
    },
  };
}

function makeClock(startAt = 1_000_000) {
  const timers = makeFakeTimers();
  let current = startAt;
  const clock = createSharedClock({
    intervalMs: 60_000,
    now: () => current,
    setTimer: timers.setTimer,
    clearTimer: timers.clearTimer,
  });
  return { clock, timers, advance: (ms: number) => (current += ms) };
}

describe('createSharedClock', () => {
  test('getSnapshot is referentially stable until a tick fires', () => {
    const { clock, timers, advance } = makeClock();
    clock.subscribe(() => {});

    const first = clock.getSnapshot();
    advance(30_000);
    // Wall time moved but no tick fired — the snapshot must not.
    expect(clock.getSnapshot()).toBe(first);
    expect(clock.getSnapshot()).toBe(first);

    timers.fireAll();
    expect(clock.getSnapshot()).toBe(first + 30_000);
  });

  test('notifies every subscriber on a tick', () => {
    const { clock, timers, advance } = makeClock();
    let a = 0;
    let b = 0;
    clock.subscribe(() => (a += 1));
    clock.subscribe(() => (b += 1));

    advance(60_000);
    timers.fireAll();

    expect(a).toBe(1);
    expect(b).toBe(1);
  });

  test('three subscribers share exactly one timer', () => {
    const { clock, timers } = makeClock();
    clock.subscribe(() => {});
    clock.subscribe(() => {});
    clock.subscribe(() => {});

    expect(clock.subscriberCount()).toBe(3);
    expect(timers.created()).toBe(1);
    expect(timers.active()).toBe(1);
  });

  test('clears the timer when the last subscriber leaves, and restarts after', () => {
    const { clock, timers } = makeClock();
    const off1 = clock.subscribe(() => {});
    const off2 = clock.subscribe(() => {});

    off1();
    expect(timers.active()).toBe(1); // still one listener
    expect(timers.cleared()).toBe(0);

    off2();
    expect(clock.subscriberCount()).toBe(0);
    expect(timers.active()).toBe(0);
    expect(timers.cleared()).toBe(1);

    clock.subscribe(() => {});
    expect(timers.created()).toBe(2);
    expect(timers.active()).toBe(1);
  });

  test('resynchronizes on the first subscribe so an idle clock is not stale', () => {
    const { clock, timers, advance } = makeClock();
    advance(3_600_000); // an hour passes with nobody watching

    clock.subscribe(() => {});
    expect(clock.getSnapshot()).toBe(1_000_000 + 3_600_000);
    expect(timers.created()).toBe(1);
  });

  test('unsubscribing twice does not clear a fresh timer', () => {
    const { clock, timers } = makeClock();
    const off = clock.subscribe(() => {});
    off();
    off();
    clock.subscribe(() => {});

    expect(timers.cleared()).toBe(1);
    expect(timers.active()).toBe(1);
  });
});
