import { describe, expect, test } from 'bun:test';
import { buildPoolMembers } from '@/features/quota/pool/members';
import { segmentValueAt } from '@/features/quota/pool/geometry';
import { recordedPool } from '@/features/quota/pool/recorded';
import {
  declinesBetween,
  memberBreakpoints,
  readingIndexAt,
  remainingAt,
  remainingFraction,
} from '@/features/quota/pool/track';
import type { PoolMember, Reading, RecordedSegment } from '@/features/quota/pool/types';
import { HOUR_MS } from '@/utils/time/durations';
import {
  CLAUDE_RESETS,
  NOW,
  claudeOutageFiles,
  historyCredential,
  historyWithOutage,
  okHistory,
  poolModelInput,
  weeklyFile,
} from './fixtures/quotaPoolFixture';

const T0 = Date.UTC(2026, 9, 20);

const reading = (t: number, used: number, resetAt: number | null = null): Reading => ({
  live: false,
  resetAt,
  t,
  used,
});

const member = (key: string, readings: Reading[]): PoolMember => ({
  file: { name: key },
  key,
  periodMs: 168 * HOUR_MS,
  readings,
});

const hours = (count: number) => T0 + count * HOUR_MS;

const point = (t: number, used: number) => ({ resetAt: null, t, used });

const riserDeltas = (segments: RecordedSegment[]) =>
  segments.flatMap(({ points }) =>
    points
      .slice(1)
      .flatMap((point, index) =>
        point.t === points[index].t ? [{ delta: point.v - points[index].v, t: point.t }] : []
      )
  );

describe('buildPoolMembers', () => {
  test('joins history by id, then by name, and counts files without readings', () => {
    const files = [
      weeklyFile('by-id.json', 0.1, CLAUDE_RESETS[0], { id: 'credential-1' }),
      { id: 'other-id', name: 'by-name.json' },
      { name: 'no-data.json' },
    ];
    const history = okHistory([
      {
        ...historyCredential('unused', 'claude', [point(NOW - HOUR_MS, 0.05)]),
        id: 'credential-1',
      },
      { ...historyCredential('by-name.json', 'claude', [point(NOW - HOUR_MS, 0.2)]), id: 'x' },
    ]);
    const { members, noDataCount } = buildPoolMembers(poolModelInput('claude', { files, history }));

    expect(members.map((entry) => entry.key)).toEqual(['by-id.json', 'by-name.json']);
    expect(members[0].readings).toEqual([
      { live: false, resetAt: null, t: NOW - HOUR_MS, used: 0.05 },
      { live: true, resetAt: CLAUDE_RESETS[0], t: NOW, used: 0.1 },
    ]);
    expect(members[1].readings).toEqual([
      { live: false, resetAt: null, t: NOW - HOUR_MS, used: 0.2 },
    ]);
    expect(noDataCount).toBe(1);
  });

  test('keeps only history before dataAt and stamps the live reading at dataAt', () => {
    const dataAt = NOW - 2 * HOUR_MS;
    const { members } = buildPoolMembers(poolModelInput('claude', { dataAt }));
    const readings = members[0].readings;

    expect(readings.slice(0, -1).every((entry) => !entry.live && entry.t < dataAt)).toBe(true);
    expect(readings[readings.length - 1]).toMatchObject({ live: true, t: dataAt });
  });
});

describe('track', () => {
  const account = member('a', [
    reading(hours(1), 0.4, hours(3)),
    reading(hours(4), 0.2, hours(40)),
  ]);

  test('is null before the first reading and full from its reset on', () => {
    expect(remainingFraction(account, hours(0))).toBeNull();
    expect(remainingAt(account, hours(1))).toBeCloseTo(60);
    expect(remainingAt(account, hours(3) - 1)).toBeCloseTo(60);
    expect(remainingFraction(account, hours(3))).toBe(1);
    expect(remainingAt(account, hours(4))).toBeCloseTo(80);
    expect(readingIndexAt(account, hours(3.5))).toBe(0);
    expect(readingIndexAt(account, hours(4))).toBe(1);
  });

  test('lists reading times and reset instants in (from, to]', () => {
    expect(memberBreakpoints(account, hours(1), hours(4))).toEqual([hours(3), hours(4)]);
    expect(memberBreakpoints(account, hours(0), hours(50))).toEqual([
      hours(1),
      hours(3),
      hours(4),
      hours(40),
    ]);
  });

  test('ignores reset jumps when summing declines', () => {
    const declines = declinesBetween(account, hours(1), hours(4));

    expect(declines.spread).toBe(false);
    expect(declines.points).toBeCloseTo(20);
  });

  test('spreads a decline across an observation gap', () => {
    const { members } = buildPoolMembers(
      poolModelInput('claude', { files: claudeOutageFiles(), history: historyWithOutage() })
    );
    const declines = declinesBetween(members[0], NOW - 3 * HOUR_MS, NOW);

    expect(declines.spread).toBe(true);
    expect(declines.points).toBeCloseTo((60 * (151 / 60)) / 48, 9);
  });

  test('spreads only the stretch after a reset that falls inside the gap', () => {
    const refilled = member('a', [reading(hours(0), 0.5, hours(2)), reading(hours(10), 0.4)]);

    expect(declinesBetween(refilled, hours(0), hours(10))).toEqual({ points: 40, spread: true });
    expect(declinesBetween(refilled, hours(6), hours(10)).points).toBeCloseTo(20);
  });
});

describe('recordedPool', () => {
  test('carries readings forward, so a missing reading draws no dip', () => {
    const first = member('a', [reading(hours(0), 0.5), reading(hours(3), 0.5)]);
    const second = member('b', [
      reading(hours(0), 0),
      reading(hours(1), 0.1),
      reading(hours(3), 0.1),
    ]);
    const { segments } = recordedPool([first, second], hours(0), hours(3));

    expect(segments).toHaveLength(1);
    expect(Math.min(...segments[0].points.map((point) => point.v))).toBe(140);
  });

  test('labels a reset step with exactly the drawn delta', () => {
    const account = member('a', [
      reading(hours(0), 0.7, hours(5)),
      reading(hours(4), 0.7, hours(5)),
      reading(hours(8), 0.1, hours(100)),
    ]);
    const { segments, steps } = recordedPool([account], hours(0), hours(8));

    expect(steps).toEqual([{ gain: 70, keys: ['a'], kind: 'recorded', t: hours(5) }]);
    expect(riserDeltas(segments)).toContainEqual({ delta: 70, t: hours(5) });
  });

  test('marks the stretch before a late member as partial without a label', () => {
    const early = member('a', [reading(hours(0), 0.5)]);
    const late = member('b', [reading(hours(4), 0)]);
    const { segments, steps } = recordedPool([early, late], hours(0), hours(8));

    expect(steps).toEqual([]);
    expect(segments.map(({ known, partial, stale }) => ({ known, partial, stale }))).toEqual([
      { known: 1, partial: true, stale: false },
      { known: 2, partial: false, stale: false },
    ]);
    expect(segments[0].points[segments[0].points.length - 1]).toEqual(segments[1].points[0]);
  });

  test('starts a new partial segment whenever coverage changes', () => {
    const hourly = (start: number) =>
      Array.from({ length: 9 - start }, (_, offset) => reading(hours(start + offset), 0.3));
    const members = [0, 2, 4].map((start, index) => member(`m${index}`, hourly(start)));
    const { segments } = recordedPool(members, hours(0), hours(8));

    expect(segments.map(({ known, partial }) => ({ known, partial }))).toEqual([
      { known: 1, partial: true },
      { known: 2, partial: true },
      { known: 3, partial: false },
    ]);
    expect(segmentValueAt(segments, hours(3))).toEqual({ known: 2, partial: true, v: 140 });
    expect(segments[0].points[segments[0].points.length - 1]).toEqual(segments[1].points[0]);
  });

  test('merges resets less than 60s apart into one step measured per member', () => {
    const offsets = [0, 20_000, 45_000];
    const members = offsets.map((offset, index) =>
      member(`m${index}`, [reading(hours(0), 0.2 * (index + 1), hours(5) + offset)])
    );
    const { segments, steps } = recordedPool(members, hours(0), hours(8));
    const riserTotal = riserDeltas(segments).reduce((sum, riser) => sum + riser.delta, 0);

    expect(steps).toHaveLength(1);
    expect(steps[0]).toMatchObject({ keys: ['m0', 'm1', 'm2'], t: hours(5) });
    expect(steps[0].gain).toBeCloseTo(120);
    expect(riserTotal).toBeCloseTo(steps[0].gain);
  });

  test('drops degenerate segments when only live readings exist', () => {
    const { members } = buildPoolMembers(poolModelInput('claude', { history: null }));

    expect(recordedPool(members, NOW - 3 * 24 * HOUR_MS, NOW).segments).toEqual([]);
  });

  test('returns nothing for an empty interval', () => {
    expect(recordedPool([member('a', [reading(hours(0), 0)])], hours(2), hours(2))).toEqual({
      segments: [],
      steps: [],
    });
  });

  test('fades a reading gap longer than STALE_GAP_MS as partial and stale', () => {
    const gappy = member('a', [
      reading(hours(0), 0.1),
      reading(hours(1), 0.1),
      reading(hours(7), 0.75),
      reading(hours(8), 0.75),
    ]);
    const { segments } = recordedPool([gappy], hours(0), hours(8));

    expect(segments.map(({ partial, stale }) => ({ partial, stale }))).toEqual([
      { partial: false, stale: false },
      { partial: true, stale: true },
      { partial: false, stale: false },
    ]);
    expect(segments[1].points[0].t).toBe(hours(1));
    expect(segments[2].points[0]).toEqual({ t: hours(7), v: 25 });
    expect(riserDeltas([segments[1]])).toEqual([{ delta: -65, t: hours(7) }]);
  });
});
