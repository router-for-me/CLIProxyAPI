import { describe, expect, test } from 'bun:test';
import {
  claudePeriodHours,
  parseIsoToMs,
  parseOffsetSecondsToMs,
  parseUnixToMs,
  periodHoursFromSeconds,
  resolveResetMs,
} from '../src/utils/quota/resetInstants';

describe('parseIsoToMs', () => {
  test('parses a plain ISO timestamp', () => {
    expect(parseIsoToMs('2026-07-29T14:59:00Z')).toBe(Date.UTC(2026, 6, 29, 14, 59));
  });

  test('tolerates over-precise fractional seconds', () => {
    // Some providers emit nanoseconds, which Date rejects on some engines.
    expect(parseIsoToMs('2026-07-29T14:59:00.123456789Z')).toBe(
      Date.UTC(2026, 6, 29, 14, 59, 0, 123)
    );
  });

  test('rejects non-strings, blanks and unparseable text', () => {
    expect(parseIsoToMs(undefined)).toBeNull();
    expect(parseIsoToMs('   ')).toBeNull();
    expect(parseIsoToMs('not a date')).toBeNull();
    expect(parseIsoToMs(1754000000)).toBeNull();
  });
});

describe('parseUnixToMs', () => {
  test('treats small magnitudes as seconds and large as milliseconds', () => {
    expect(parseUnixToMs(1_754_000_000)).toBe(1_754_000_000_000);
    expect(parseUnixToMs(1_754_000_000_000)).toBe(1_754_000_000_000);
  });

  test('accepts numeric strings', () => {
    expect(parseUnixToMs('1754000000')).toBe(1_754_000_000_000);
  });

  test('rejects zero, negatives and junk', () => {
    expect(parseUnixToMs(0)).toBeNull();
    expect(parseUnixToMs(-5)).toBeNull();
    expect(parseUnixToMs('soon')).toBeNull();
    expect(parseUnixToMs(null)).toBeNull();
  });
});

describe('parseOffsetSecondsToMs', () => {
  test('projects a countdown forward from now', () => {
    const now = 1_754_000_000_000;
    expect(parseOffsetSecondsToMs(3600, now)).toBe(now + 3_600_000);
  });

  test('rejects a non-positive or unparseable offset', () => {
    expect(parseOffsetSecondsToMs(0, 1000)).toBeNull();
    expect(parseOffsetSecondsToMs(-60, 1000)).toBeNull();
    expect(parseOffsetSecondsToMs('nope', 1000)).toBeNull();
  });
});

describe('resolveResetMs', () => {
  test('takes the first parseable candidate, ISO or Unix', () => {
    expect(resolveResetMs([undefined, null, '2026-07-29T14:59:00Z'])).toBe(
      Date.UTC(2026, 6, 29, 14, 59)
    );
    expect(resolveResetMs([undefined, 1_754_000_000])).toBe(1_754_000_000_000);
  });

  test('prefers an earlier candidate over a later one in the list', () => {
    // Order is priority, not chronology — callers list their preferred key first.
    expect(resolveResetMs(['2026-01-01T00:00:00Z', 1_754_000_000])).toBe(
      Date.UTC(2026, 0, 1)
    );
  });

  test('returns null when nothing parses', () => {
    expect(resolveResetMs([])).toBeNull();
    expect(resolveResetMs([undefined, null, '', 'later'])).toBeNull();
  });
});

describe('period derivation', () => {
  test('converts a window length in seconds to hours', () => {
    expect(periodHoursFromSeconds(18_000)).toBe(5);
    expect(periodHoursFromSeconds(604_800)).toBe(168);
    expect(periodHoursFromSeconds('18000')).toBe(5);
  });

  test('rejects an absent or non-positive window length', () => {
    expect(periodHoursFromSeconds(0)).toBeNull();
    expect(periodHoursFromSeconds(undefined)).toBeNull();
  });

  test('derives the Claude period from its window key', () => {
    // Claude states the period nowhere in the payload — only the key implies it.
    expect(claudePeriodHours('five_hour')).toBe(5);
    expect(claudePeriodHours('seven_day')).toBe(168);
    expect(claudePeriodHours('seven_day_opus')).toBe(168);
  });
});
