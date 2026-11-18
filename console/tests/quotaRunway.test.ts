import { describe, expect, test } from 'bun:test';
import type { LedgerRow } from '@/features/quota/ledger/ledgerModel';
import {
  RUNWAY_SPAN_MS,
  placeRunwayMarkers,
  runwayDayTicks,
} from '@/features/quota/ledger/runwayModel';

const HOUR = 3_600_000;
const NOW = 1_800_000_000_000;

const row = (name: string, resetInHours: number | null, loaded = true): LedgerRow => ({
  entry: { type: 'claude', file: { name } },
  key: name,
  status: loaded ? 'success' : 'loading',
  meters: [],
  primary: loaded
    ? {
        id: 'seven-day',
        labelKey: 'x',
        remaining: 50,
        resetAtMs: resetInHours === null ? null : NOW + resetInHours * HOUR,
        resetLabel: '',
        muted: false,
      }
    : null,
  planType: null,
  renewsAt: null,
  resetCredits: null,
  nextUp: false,
});

describe('runway markers', () => {
  test('place accounts along seven days and stack the ones that collide', () => {
    const markers = placeRunwayMarkers(
      [
        row('late', 84),
        row('soon', 12),
        row('near-soon', 13),
        row('unseen', null),
        row('wait', 0, false),
      ],
      NOW
    );
    expect(markers.map((m) => [m.row.key, m.x, m.level])).toEqual([
      ['soon', (12 * HOUR) / RUNWAY_SPAN_MS, 0],
      ['near-soon', (13 * HOUR) / RUNWAY_SPAN_MS, 1],
      ['late', 0.5, 0],
      ['unseen', null, 0],
    ]);
  });

  test('a reset already in the past counts as unseen', () => {
    expect(placeRunwayMarkers([row('stale', -1)], NOW)[0].x).toBeNull();
  });
});

describe('runway day ticks', () => {
  test('one tick per local midnight inside the span', () => {
    const ticks = runwayDayTicks(NOW);
    expect(ticks.length).toBe(7);
    for (const tick of ticks) {
      expect(new Date(tick.atMs).getHours()).toBe(0);
      expect(tick.x).toBeGreaterThan(0);
      expect(tick.x).toBeLessThan(1);
    }
  });
});
