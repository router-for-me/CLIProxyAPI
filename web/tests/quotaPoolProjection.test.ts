import { describe, expect, test } from 'bun:test';
import { MAX_SIM_EVENTS } from '@/features/quota/pool/constants';
import { formatWeekdayTime } from '@/features/quota/pool/format';
import { buildPoolMembers } from '@/features/quota/pool/members';
import { projectPool } from '@/features/quota/pool/projection';
import type { PoolMember, Projection, Reading } from '@/features/quota/pool/types';
import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/utils/time/durations';
import {
  CLAUDE_RESETS,
  CODEX_EMPTY,
  CODEX_RATE,
  CODEX_RESET,
  NOW,
  TZ,
  claudeName,
  codexName,
  poolModelInput,
} from './fixtures/quotaPoolFixture';

const HORIZON = NOW + 14 * DAY_MS;

const membersFor = (provider: 'claude' | 'codex') =>
  buildPoolMembers(poolModelInput(provider)).members;

const member = (key: string, used: number, resetAt: number | null): PoolMember => {
  const readings: Reading[] = [{ live: true, resetAt, t: NOW, used }];

  return {
    file: { name: key },
    key,
    ordinal: 1,
    periodMs: 168 * HOUR_MS,
    readings,
  };
};

const stepGains = (projection: Projection) => projection.steps.map((step) => Math.round(step.gain));

describe('projectPool on the fixture', () => {
  test('Claude: first empty in 15h 49m, steps +100 +97 +60 +48, never empty', () => {
    const projection = projectPool(membersFor('claude'), NOW, 3.7, HORIZON);

    expect(projection.firstEmpty?.key).toBe(claudeName('a'));
    expect(Math.round(((projection.firstEmpty?.at ?? 0) - NOW) / MINUTE_MS)).toBe(15 * 60 + 49);
    expect(stepGains(projection).slice(0, 4)).toEqual([100, 97, 60, 48]);
    expect(projection.steps.slice(0, 4).map((step) => step.t)).toEqual(CLAUDE_RESETS.slice(0, 4));
    expect(projection.poolEmptyAt).toBeNull();
  });

  test('Codex: empty at Sun 4:39 PM, then one merged +300 step', () => {
    const projection = projectPool(membersFor('codex'), NOW, CODEX_RATE, HORIZON);
    const firstStep = projection.steps[0];

    expect(projection.poolEmptyAt).toBeCloseTo(CODEX_EMPTY, -1);
    expect(formatWeekdayTime(projection.poolEmptyAt ?? 0, 'en', TZ)).toBe('Sun 4:39 PM');
    expect(projection.firstEmpty?.key).toBe(codexName('b'));
    expect(Math.floor(((projection.firstEmpty?.at ?? 0) - NOW) / HOUR_MS)).toBe(58);
    expect(firstStep).toMatchObject({ kind: 'projected', t: CODEX_RESET });
    expect(firstStep.gain).toBeCloseTo(300, 9);
    expect(firstStep.keys).toHaveLength(3);
  });
});

describe('projectPool invariants', () => {
  const projection = projectPool(membersFor('claude'), NOW, 3.7, HORIZON);

  test('falls linearly at the rate between events', () => {
    const slopes = projection.points.slice(1).flatMap((end, index) => {
      const start = projection.points[index];

      return end.t > start.t && end.v < start.v
        ? [(start.v - end.v) / ((end.t - start.t) / HOUR_MS)]
        : [];
    });

    expect(slopes.length).toBeGreaterThan(0);
    expect(slopes.every((slope) => Math.abs(slope - 3.7) < 1e-6)).toBe(true);
  });

  test('stays within [0, capacity] and ends at the end instant', () => {
    expect(projection.points.every((point) => point.v >= 0 && point.v <= 600 + 1e-9)).toBe(true);
    expect(projection.points[projection.points.length - 1].t).toBe(HORIZON);
  });

  test('draws only risers at rate 0 and never empties', () => {
    const idle = projectPool(membersFor('claude'), NOW, 0, HORIZON);
    const risesOnly = idle.points
      .slice(1)
      .every(
        (point, index) => point.t === idle.points[index].t || point.v === idle.points[index].v
      );

    expect(risesOnly).toBe(true);
    expect(idle.poolEmptyAt).toBeNull();
    expect(idle.firstEmpty).toBeNull();
    expect(stepGains(idle).slice(0, 2)).toEqual([42, 0]);
  });

  test('never refills a member without a reset and drains it last', () => {
    const projection = projectPool(
      [member('no-reset', 0, null), member('resets', 0.5, NOW + 10 * HOUR_MS)],
      NOW,
      10,
      NOW + 30 * HOUR_MS
    );

    expect(projection.firstEmpty).toEqual({ at: NOW + 5 * HOUR_MS, key: 'resets' });
    expect(projection.steps.map((step) => step.keys)).toEqual([['resets']]);
    expect(projection.poolEmptyAt).toBe(NOW + 25 * HOUR_MS);
  });

  test('settles 50 accounts in fewer than MAX_SIM_EVENTS events', () => {
    const members = Array.from({ length: 50 }, (_, index) =>
      member(`m${String(index).padStart(2, '0')}`, (index % 10) / 10, NOW + index * 3 * HOUR_MS)
    );
    const projection = projectPool(members, NOW, 40, HORIZON);

    expect(projection.points.length).toBeLessThan(MAX_SIM_EVENTS);
    expect(projection.points[projection.points.length - 1].t).toBe(HORIZON);
  });

  test('is not empty when it hits 0 within 60s before a reset or exactly at it', () => {
    const resetAt = NOW + 10 * HOUR_MS;
    const almost = projectPool(
      [member('a', 0, resetAt)],
      NOW,
      100 / (10 - 1 / 120),
      resetAt + HOUR_MS
    );
    const exact = projectPool([member('a', 0, resetAt)], NOW, 10, resetAt + HOUR_MS);
    const early = projectPool([member('a', 0, resetAt)], NOW, 100 / 9, resetAt + HOUR_MS);

    expect(almost.poolEmptyAt).toBeNull();
    expect(exact.poolEmptyAt).toBeNull();
    expect(early.poolEmptyAt).toBeCloseTo(NOW + 9 * HOUR_MS, -1);
    expect(almost.firstEmpty).toBeNull();
    expect(exact.firstEmpty).toBeNull();
    expect(early.firstEmpty?.at).toBe(early.poolEmptyAt ?? undefined);
  });

  test('a single account empties first exactly when the pool does', () => {
    const resetAt = NOW + 10 * HOUR_MS;

    for (const rate of [5, 50 / 9.99, 50 / 9.9]) {
      const projection = projectPool([member('a', 0.5, resetAt)], NOW, rate, HORIZON);

      expect(projection.firstEmpty?.at).toBe(projection.poolEmptyAt ?? undefined);
    }
  });

  test('is empty at the start when nothing remains', () => {
    const projection = projectPool([member('a', 1, NOW + HOUR_MS)], NOW, 1, NOW + 2 * HOUR_MS);

    expect(projection.poolEmptyAt).toBe(NOW);
    expect(projection.firstEmpty).toEqual({ at: NOW, key: 'a' });
  });
});
