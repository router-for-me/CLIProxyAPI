import { describe, expect, test } from 'bun:test';
import { burnRate, drainProjected, drainRecorded } from '@/features/quota/pool/burn';
import { DRAIN_GRID_MS } from '@/features/quota/pool/constants';
import { buildPoolMembers } from '@/features/quota/pool/members';
import { projectPool } from '@/features/quota/pool/projection';
import type { PoolModelInput } from '@/features/quota/pool/types';
import { HOUR_MS, MINUTE_MS } from '@/utils/time/durations';
import {
  CODEX_EMPTY,
  CODEX_RATE,
  CODEX_RESET,
  NOW,
  claudeName,
  claudeOutageFiles,
  historyCredential,
  historyWithOutage,
  okHistory,
  poolModelInput,
} from './fixtures/quotaPoolFixture';

const membersFor = (provider: 'claude' | 'codex', overrides: Partial<PoolModelInput> = {}) =>
  buildPoolMembers(poolModelInput(provider, overrides)).members;

describe('burnRate', () => {
  test('matches the Claude fixture over 1h, 3h and 6h', () => {
    const members = membersFor('claude');

    expect(burnRate(members, NOW, 3 * HOUR_MS)).toEqual({
      approximate: false,
      coveredMs: 3 * HOUR_MS,
      value: expect.closeTo(3.7, 9),
    });
    expect(burnRate(members, NOW, HOUR_MS).value).toBeCloseTo(5.55, 9);
    expect(burnRate(members, NOW, 6 * HOUR_MS).value).toBeCloseTo(1.85, 9);
  });

  test('matches the Codex fixture over 3h', () => {
    expect(burnRate(membersFor('codex'), NOW, 3 * HOUR_MS).value).toBeCloseTo(CODEX_RATE, 9);
  });

  test('is approximate when history covers less than the window', () => {
    const history = okHistory([
      historyCredential(claudeName('a'), 'claude', [
        { resetAt: null, t: NOW - 2 * HOUR_MS, used: 0.3 },
        { resetAt: null, t: NOW - HOUR_MS, used: 0.35 },
      ]),
    ]);
    const burn = burnRate(membersFor('claude', { history }), NOW, 6 * HOUR_MS);

    expect(burn.approximate).toBe(true);
    expect(burn.coveredMs).toBe(2 * HOUR_MS);
    expect(burn.value).toBeCloseTo((70 - 58.5) / 2, 9);
  });

  test('is unknown below 15 minutes of history and with live readings only', () => {
    const history = okHistory([
      historyCredential(claudeName('a'), 'claude', [
        { resetAt: null, t: NOW - 10 * MINUTE_MS, used: 0.3 },
      ]),
    ]);

    expect(burnRate(membersFor('claude', { history }), NOW, 3 * HOUR_MS)).toEqual({
      approximate: false,
      coveredMs: 10 * MINUTE_MS,
      value: null,
    });
    expect(burnRate(membersFor('claude', { history: null }), NOW, 3 * HOUR_MS)).toEqual({
      approximate: false,
      coveredMs: 0,
      value: null,
    });
  });

  test('spreads an outage decline instead of reading 20%/h', () => {
    const members = membersFor('claude', {
      files: claudeOutageFiles(),
      history: historyWithOutage(),
    });
    const burn = burnRate(members, NOW, 3 * HOUR_MS);

    expect(burn.approximate).toBe(true);
    expect(burn.value).toBeCloseTo((60 * (151 / 60)) / 48 / 3, 9);
    expect(burn.value).toBeCloseTo(1.05, 2);
  });
});

describe('drainRecorded', () => {
  test('draws one gap-free polyline on the 15-minute grid when history covers it', () => {
    const from = NOW - 24 * HOUR_MS;
    const segments = drainRecorded(membersFor('claude'), from, NOW, 3 * HOUR_MS);
    const points = segments[0].points;

    expect(segments).toHaveLength(1);
    expect(segments[0].partial).toBe(false);
    expect(points[0].t).toBe(Math.ceil(from / DRAIN_GRID_MS) * DRAIN_GRID_MS);
    expect(points[points.length - 1]).toEqual({ t: NOW, v: expect.closeTo(3.7, 9) });
    expect(points.every((point) => point.v >= 0)).toBe(true);
  });

  test('leaves a gap where the rate is unknown', () => {
    const history = okHistory([
      historyCredential(claudeName('a'), 'claude', [
        { resetAt: null, t: NOW - 2 * HOUR_MS, used: 0.3 },
      ]),
    ]);
    const segments = drainRecorded(
      membersFor('claude', { history }),
      NOW - 4 * HOUR_MS,
      NOW,
      3 * HOUR_MS
    );

    expect(segments).toHaveLength(1);
    expect(segments[0].points[0].t).toBe(NOW - 2 * HOUR_MS + 15 * MINUTE_MS + 8 * MINUTE_MS);
  });
});

describe('drainProjected', () => {
  test('is 0 from the empty instant until the reset and skips risers', () => {
    const members = membersFor('codex');
    const projection = projectPool(members, NOW, CODEX_RATE, NOW + 14 * 24 * HOUR_MS);
    const drain = drainProjected(projection);
    const emptyIndex = drain.findIndex((point) => point.t === CODEX_EMPTY && point.v === 0);

    expect(drain.slice(emptyIndex, emptyIndex + 2)).toEqual([
      { t: CODEX_EMPTY, v: 0 },
      { t: CODEX_RESET, v: 0 },
    ]);
    expect(drain[emptyIndex + 2]).toEqual({ t: CODEX_RESET, v: expect.closeTo(CODEX_RATE, 9) });
    expect(drain[0]).toEqual({ t: NOW, v: expect.closeTo(CODEX_RATE, 9) });
    expect(drain.every((point) => point.v <= CODEX_RATE + 1e-9)).toBe(true);
  });
});
