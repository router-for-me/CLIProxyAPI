/**
 * A single ticking clock shared by every component that renders a countdown.
 *
 * Relative time ("in 3 hours") goes stale on its own — nothing re-renders when
 * a minute passes, so a long-lived tab silently lies. The naive fix is a timer
 * per component, which on the quota page means one per card body, all firing at
 * slightly different offsets and each scheduling its own wake-up.
 *
 * This is one timer for the whole app instead, started when the first consumer
 * subscribes and cleared when the last one leaves. Idle pages pay nothing.
 *
 * React-free by design: the store contract (`subscribe` / `getSnapshot`) is
 * exactly what `useSyncExternalStore` wants, and the timer functions are
 * injectable so tests never touch a real clock.
 */

import { MINUTE_MS } from './durations';

export interface SharedClock {
  subscribe(listener: () => void): () => void;
  /**
   * The current instant.
   *
   * MUST be referentially stable between ticks. Returning a fresh `Date.now()`
   * on every call makes React's `useSyncExternalStore` see a changed snapshot
   * on every render, warn ("The result of getSnapshot should be cached"), and
   * re-render forever. The value is therefore cached and only advanced by the
   * timer callback.
   */
  getSnapshot(): number;
}

export interface SharedClockOptions {
  intervalMs?: number;
  now?: () => number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (id: unknown) => void;
}

export interface TestableSharedClock extends SharedClock {
  /** Live subscriber count — exposed so tests can assert timer lifecycle. */
  subscriberCount(): number;
}

export function createSharedClock(options: SharedClockOptions = {}): TestableSharedClock {
  const {
    intervalMs = MINUTE_MS,
    now = Date.now,
    setTimer = (fn, ms) => setInterval(fn, ms),
    clearTimer = (id) => clearInterval(id as ReturnType<typeof setInterval>),
  } = options;

  const listeners = new Set<() => void>();
  let current = now();
  let timerId: unknown = null;

  const tick = () => {
    current = now();
    listeners.forEach((listener) => listener());
  };

  return {
    subscribe(listener: () => void) {
      listeners.add(listener);
      if (timerId === null) {
        // First subscriber: resynchronize before starting, so a clock that sat
        // idle for an hour doesn't hand out a stale snapshot for one interval.
        current = now();
        timerId = setTimer(tick, intervalMs);
      }
      return () => {
        listeners.delete(listener);
        if (listeners.size === 0 && timerId !== null) {
          clearTimer(timerId);
          timerId = null;
        }
      };
    },
    getSnapshot() {
      return current;
    },
    subscriberCount() {
      return listeners.size;
    },
  };
}

/** The app-wide minute clock. Import this rather than making another one. */
export const MINUTE_CLOCK: SharedClock = createSharedClock();
