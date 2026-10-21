/**
 * Timezone labelling, and the guard against the Asia/Shanghai hardcode
 * returning.
 *
 * Codex reset-credit expiry was rendered in a fixed GMT+8 while every other
 * timestamp on the same page used the browser's timezone — so one credit
 * appeared twice, in two timezones, on one screen.
 */

import { describe, expect, test } from 'bun:test';
import i18n from '@/i18n';
import * as resetCredits from '@/utils/quota/resetCredits';
import { formatUtcOffsetLabel, resolveTimeZoneLabel } from '@/utils/time/timezone';

describe('formatUtcOffsetLabel', () => {
  test('renders whole-hour offsets east and west of UTC', () => {
    expect(formatUtcOffsetLabel(480)).toBe('GMT+8');
    expect(formatUtcOffsetLabel(-300)).toBe('GMT-5');
    expect(formatUtcOffsetLabel(720)).toBe('GMT+12');
    expect(formatUtcOffsetLabel(-720)).toBe('GMT-12');
  });

  test('renders half- and quarter-hour offsets', () => {
    expect(formatUtcOffsetLabel(330)).toBe('GMT+5:30');
    expect(formatUtcOffsetLabel(-570)).toBe('GMT-9:30');
    expect(formatUtcOffsetLabel(345)).toBe('GMT+5:45');
  });

  test('UTC itself carries no sign', () => {
    expect(formatUtcOffsetLabel(0)).toBe('GMT');
    expect(formatUtcOffsetLabel(-0)).toBe('GMT');
  });

  test('a non-finite offset degrades to a bare GMT rather than "GMT+NaN"', () => {
    expect(formatUtcOffsetLabel(Number.NaN)).toBe('GMT');
  });
});

describe('resolveTimeZoneLabel', () => {
  test('produces a well-formed label whatever timezone the runner is in', () => {
    expect(resolveTimeZoneLabel()).toMatch(/^GMT([+-]\d{1,2}(:\d{2})?)?$/);
  });

  test('reflects the offset of the instant it is given, not of "now"', () => {
    // A DST-observing zone answers differently in January than in July, so the
    // label has to follow the date the timestamps are being rendered for.
    const january = new Date(2026, 0, 15, 12);
    const july = new Date(2026, 6, 15, 12);
    expect(resolveTimeZoneLabel(january)).toBe(
      formatUtcOffsetLabel(-january.getTimezoneOffset())
    );
    expect(resolveTimeZoneLabel(july)).toBe(formatUtcOffsetLabel(-july.getTimezoneOffset()));
  });
});

describe('Asia/Shanghai hardcode', () => {
  test('is gone from the reset-credit module', () => {
    expect('formatShanghaiDateTime' in resetCredits).toBe(false);
    expect(resetCredits.normalizeCodexResetCreditsPayload).toBeDefined();
  });

  test('the expiry heading interpolates a timezone in all four locales', async () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      await i18n.changeLanguage(locale);
      const label = i18n.t('codex_quota.reset_credits_expiry_label', { timezone: 'GMT+8' });
      expect(label).toContain('GMT+8');
      expect(label).not.toContain('{{');
    }
    await i18n.changeLanguage('en');
  });
});
