import { expect, test } from 'bun:test';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';

test('provider advanced editors have complete translations in all four languages', () => {
  for (const section of ['runtimePolicy', 'modelOptions', 'behavior'] as const) {
    const keys = Object.keys(en.providersPage[section]).sort();
    expect(keys.length).toBeGreaterThan(0);
    for (const locale of [zhCN, zhTW, ru]) {
      expect(Object.keys(locale.providersPage[section]).sort()).toEqual(keys);
      expect(
        Object.values(locale.providersPage[section]).every(
          (value) => typeof value === 'string' && value.trim().length > 0
        )
      ).toBe(true);
    }
  }
});
