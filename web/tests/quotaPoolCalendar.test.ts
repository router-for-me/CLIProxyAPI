import { describe, expect, test } from 'bun:test';
import {
  addZonedDays,
  historySinceMs,
  poolRange,
  zonedMidnight,
  zonedOffsetMs,
} from '@/features/quota/pool/calendar';
import { HOUR_MS } from '@/utils/time/durations';
import { NOW, TZ } from './fixtures/quotaPoolFixture';

describe('poolRange', () => {
  test('spans 14 local days from midnight three days before today', () => {
    const range = poolRange(NOW, 0, TZ);

    expect(range.start).toBe(Date.UTC(2026, 9, 23, 7));
    expect(range.end).toBe(Date.UTC(2026, 10, 6, 8));
    expect(range.days).toHaveLength(14);
    expect(range.days[0].start).toBe(range.start);
    expect(range.days[13].end).toBe(range.end);
    expect(range).toMatchObject({ canNext: true, canPrev: true, isToday: true, offset: 0 });
  });

  test('makes the fall-back day 25h and every other day 24h', () => {
    const widths = poolRange(NOW, 0, TZ).days.map((day) => (day.end - day.start) / HOUR_MS);
    const fallBack = poolRange(NOW, 0, TZ).days.findIndex(
      (day) => day.start === Date.UTC(2026, 10, 1, 7)
    );

    expect(fallBack).toBe(9);
    expect(widths[fallBack]).toBe(25);
    expect(widths.filter((width) => width === 24)).toHaveLength(13);
  });

  test('moves by whole weeks and clamps the offset to [-3, 1]', () => {
    const today = poolRange(NOW, 0, TZ);
    const earliest = poolRange(NOW, -7, TZ);
    const latest = poolRange(NOW, 5, TZ);

    expect(poolRange(NOW, -1, TZ).start).toBe(addZonedDays(today.start, -7, TZ));
    expect(earliest).toMatchObject({ canNext: true, canPrev: false, isToday: false, offset: -3 });
    expect(earliest.start).toBe(addZonedDays(today.start, -21, TZ));
    expect(latest).toMatchObject({ canNext: false, canPrev: true, isToday: false, offset: 1 });
    expect(latest.start).toBe(Date.UTC(2026, 9, 30, 7));
  });
});

describe('historySinceMs', () => {
  test('uses the range start when it is earlier than 7h before now', () => {
    expect(historySinceMs(poolRange(NOW, 0, TZ), NOW)).toBe(Date.UTC(2026, 9, 23, 7));
  });

  test('uses 7h before now, floored to the hour, when the range starts later', () => {
    expect(historySinceMs(poolRange(NOW, 1, TZ), NOW)).toBe(Date.UTC(2026, 9, 26, 3));
  });
});

describe('zoned midnights', () => {
  test('finds local midnight on both sides of the DST change', () => {
    expect(zonedMidnight(Date.UTC(2026, 10, 1, 12), TZ)).toBe(Date.UTC(2026, 10, 1, 7));
    expect(zonedMidnight(Date.UTC(2026, 10, 2, 12), TZ)).toBe(Date.UTC(2026, 10, 2, 8));
    expect(zonedMidnight(Date.UTC(2026, 2, 8, 12), TZ)).toBe(Date.UTC(2026, 2, 8, 8));
    expect(zonedMidnight(Date.UTC(2026, 2, 9, 12), TZ)).toBe(Date.UTC(2026, 2, 9, 7));
  });

  test('starts a day whose midnight is skipped by DST at the transition', () => {
    const santiago = 'America/Santiago';

    expect(zonedMidnight(Date.UTC(2026, 8, 6, 18), santiago)).toBe(Date.UTC(2026, 8, 6, 4));
    expect(zonedMidnight(Date.UTC(2026, 2, 8, 18), 'America/Havana')).toBe(Date.UTC(2026, 2, 8, 5));
    expect(zonedMidnight(Date.UTC(2026, 3, 5, 18), santiago)).toBe(Date.UTC(2026, 3, 5, 4));
    expect(zonedMidnight(Date.UTC(2026, 2, 29, 12), 'Asia/Beirut')).toBe(Date.UTC(2026, 2, 28, 22));

    const days = poolRange(Date.UTC(2026, 8, 8, 18), 0, santiago).days.map((day) => [
      new Date(day.start).toLocaleDateString('en-US', { day: 'numeric', timeZone: santiago }),
      (day.end - day.start) / HOUR_MS,
    ]);

    expect(days.slice(0, 4)).toEqual([
      ['5', 24],
      ['6', 23],
      ['7', 24],
      ['8', 24],
    ]);
  });

  test('reads the local offset at an instant', () => {
    expect(zonedOffsetMs(NOW, TZ)).toBe(-7 * HOUR_MS);
    expect(zonedOffsetMs(Date.UTC(2026, 10, 2, 12), TZ)).toBe(-8 * HOUR_MS);
    expect(zonedOffsetMs(NOW, 'UTC')).toBe(0);
  });

  test('adds calendar days across DST', () => {
    expect(addZonedDays(Date.UTC(2026, 9, 31, 7), 2, TZ)).toBe(Date.UTC(2026, 10, 2, 8));
    expect(addZonedDays(Date.UTC(2026, 10, 2, 8), -2, TZ)).toBe(Date.UTC(2026, 9, 31, 7));
  });
});
