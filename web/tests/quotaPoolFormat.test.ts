import { describe, expect, test } from 'bun:test';
import {
  dayLabelParts,
  formatMonthDay,
  formatPoolPercent,
  formatRate,
  formatWeekdayTime,
  formatWhen,
  normalizeSpaces,
  splitDuration,
} from '@/features/quota/pool/format';
import { DAY_MS, HOUR_MS, MINUTE_MS } from '@/utils/time/durations';
import { CODEX_EMPTY, NOW, TZ } from './fixtures/quotaPoolFixture';

describe('splitDuration', () => {
  test('splits into the parts the duration keys choose from', () => {
    expect(splitDuration(15 * HOUR_MS + 48.65 * MINUTE_MS)).toEqual({ d: 0, h: 15, m: 49 });
    expect(splitDuration(DAY_MS + 13 * HOUR_MS + 28.7 * MINUTE_MS)).toEqual({ d: 1, h: 13, m: 29 });
    expect(splitDuration(49 * MINUTE_MS)).toEqual({ d: 0, h: 0, m: 49 });
    expect(splitDuration(20_000)).toEqual({ d: 0, h: 0, m: 0 });
  });
});

describe('date formatting', () => {
  test('formats weekday times, month days and day labels in the given zone', () => {
    expect(formatWeekdayTime(CODEX_EMPTY, 'en', TZ)).toBe('Sun 4:39 PM');
    expect(formatWeekdayTime(Date.UTC(2026, 9, 27, 17), 'en', TZ)).toBe('Tue 10:00 AM');
    expect(formatMonthDay(NOW + 14 * DAY_MS, 'en', TZ)).toBe('Nov 9');
    expect(dayLabelParts(Date.UTC(2026, 9, 23, 19), 'en', TZ)).toEqual({
      day: '23',
      weekday: 'Fri',
    });
  });

  test('uses the weekday within a week and the month day beyond it', () => {
    expect(formatWhen(CODEX_EMPTY, NOW, 'en', TZ)).toBe('Sun 4:39 PM');
    expect(formatWhen(Date.UTC(2026, 10, 10, 0, 39), NOW, 'en', TZ)).toBe('Nov 9, 4:39 PM');
  });

  test('replaces narrow and regular no-break spaces with plain spaces', () => {
    expect(normalizeSpaces('4:39\u202fPM\u00a0Sun')).toBe('4:39 PM Sun');
  });
});

describe('number formatting', () => {
  test('shows rates with one decimal below 10 and whole numbers above', () => {
    expect(formatRate(3.7)).toBe('3.7');
    expect(formatRate(2.6682)).toBe('2.7');
    expect(formatRate(0)).toBe('0.0');
    expect(formatRate(12.4)).toBe('12');
  });

  test('rounds pool values to whole percents', () => {
    expect(formatPoolPercent(554.6)).toBe('555');
    expect(formatPoolPercent(-0.2)).toBe('0');
  });
});
