import { describe, expect, test } from 'bun:test';
import { getNextAntigravityCountdownUpdateDelay } from '@/features/quota/providers/antigravity/countdown';

const MINUTE_MS = 60_000;

describe('Antigravity quota countdown scheduling', () => {
  test('updates at the next rounded-minute boundary', () => {
    expect(getNextAntigravityCountdownUpdateDelay([10.5 * MINUTE_MS], 0)).toBe(30_000);
    expect(getNextAntigravityCountdownUpdateDelay([10 * MINUTE_MS], 0)).toBe(MINUTE_MS);
  });

  test('uses the earliest boundary across quota buckets', () => {
    expect(getNextAntigravityCountdownUpdateDelay([10.75 * MINUTE_MS, 3.25 * MINUTE_MS], 0)).toBe(
      15_000
    );
  });

  test('stops scheduling after all reset times expire', () => {
    expect(getNextAntigravityCountdownUpdateDelay([0, Number.NaN], MINUTE_MS)).toBeNull();
  });
});
