import { describe, expect, test } from 'bun:test';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import vi from '@/i18n/locales/vi.json';
import ko from '@/i18n/locales/ko.json';
import tr from '@/i18n/locales/tr.json';
import { resolvePlanTier } from '@/utils/quota';

describe('Codex Pro display names', () => {
  for (const [locale, messages] of Object.entries({ en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru })) {
    test(`${locale} labels Pro plans as Pro 100 and Pro 200`, () => {
      expect(messages.codex_quota.plan_prolite).toBe('Pro 100');
      expect(messages.codex_quota.plan_pro).toBe('Pro 200');
    });
  }

  for (const [locale, messages] of Object.entries({
    en,
    'zh-CN': zhCN,
    'zh-TW': zhTW,
    ru,
    vi,
    ko,
    tr,
  })) {
    test(`${locale} labels Pro Max as Pro 500`, () => {
      expect(messages.codex_quota.plan_promax).toBe('Pro 500');
    });
  }
});

describe('resolvePlanTier', () => {
  test('only Pro 500 is elite, in every spelling', () => {
    expect(resolvePlanTier('promax')).toBe('elite');
    expect(resolvePlanTier('  ProMax  ')).toBe('elite');
    expect(resolvePlanTier('pro-max')).toBe('elite');
    expect(resolvePlanTier('pro_max')).toBe('elite');
  });

  test('Pro 200 and Pro 100 share the premium tier', () => {
    expect(resolvePlanTier('pro')).toBe('premium');
    expect(resolvePlanTier('  PRO  ')).toBe('premium');
    expect(resolvePlanTier('prolite')).toBe('premium');
    expect(resolvePlanTier('Pro-Lite')).toBe('premium');
    expect(resolvePlanTier('pro_lite')).toBe('premium');
  });

  test('Business plans, including Business Premium, rank with Plus', () => {
    expect(resolvePlanTier('self_serve_business_prolite')).toBe('plain');
    expect(resolvePlanTier('  SELF_SERVE_BUSINESS_PROLITE  ')).toBe('plain');
    expect(resolvePlanTier('self_serve_business_usage_based')).toBe('plain');
  });

  test('maps ordinary and unknown plans to plain', () => {
    expect(resolvePlanTier('plus')).toBe('plain');
    expect(resolvePlanTier('team')).toBe('plain');
    expect(resolvePlanTier('free')).toBe('plain');
    expect(resolvePlanTier('enterprise')).toBe('plain');
  });

  test('maps missing values to plain', () => {
    expect(resolvePlanTier(null)).toBe('plain');
    expect(resolvePlanTier(undefined)).toBe('plain');
    expect(resolvePlanTier('')).toBe('plain');
    expect(resolvePlanTier('   ')).toBe('plain');
  });
});
