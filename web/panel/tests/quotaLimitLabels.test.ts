import { describe, expect, test } from 'bun:test';

const LOCALES = ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr'];

type Messages = Record<string, Record<string, string>>;

describe('quota limit labels use one wording across providers', () => {
  for (const locale of LOCALES) {
    test(`${locale}: the same kind of limit has the same name on every provider`, async () => {
      const m = (await Bun.file(`src/i18n/locales/${locale}.json`).json()) as Messages;
      // Codex and Antigravity labels are the reference wording.
      const fiveHour = m.codex_quota.primary_window;
      const weekly = m.codex_quota.secondary_window;
      const daily = m.antigravity_quota.daily_limit;
      expect(m.antigravity_quota.five_hour_limit).toBe(fiveHour);
      expect(m.antigravity_quota.weekly_limit).toBe(weekly);

      expect(m.claude_quota.five_hour).toBe(fiveHour);
      expect(m.claude_quota.seven_day).toBe(weekly);
      expect(m.kimi_quota.weekly_limit).toBe(weekly);
      expect(m.xai_quota.weekly_limit).toBe(weekly);
      expect(m.devin_quota.weekly).toBe(weekly);
      expect(m.devin_quota.daily).toBe(daily);
      expect(m.meta_quota.weekly).toBe(weekly);
    });
  }
});
