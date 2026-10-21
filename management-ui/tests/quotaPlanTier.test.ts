import { describe, expect, test } from 'bun:test';
import en from '@/i18n/locales/en.json';
import zhCN from '@/i18n/locales/zh-CN.json';
import zhTW from '@/i18n/locales/zh-TW.json';
import ru from '@/i18n/locales/ru.json';
import {
  ELITE_CODEX_PLAN_TYPE,
  PREMIUM_CODEX_PLAN_TYPES,
  resolvePlanTier,
} from '@/utils/quota';

describe('Codex Pro display names', () => {
  for (const [locale, messages] of Object.entries({ en, 'zh-CN': zhCN, 'zh-TW': zhTW, ru })) {
    test(`${locale} labels Pro plans as Pro 100 and Pro 200`, () => {
      expect(messages.codex_quota.plan_prolite).toBe('Pro 100');
      expect(messages.codex_quota.plan_pro).toBe('Pro 200');
    });
  }
});

describe('resolvePlanTier', () => {
  test("elite wins for 'pro' even though it is also in the premium set (order contract)", () => {
    // 顺序契约回归：'pro' 同时命中 PREMIUM_CODEX_PLAN_TYPES，
    // 一旦 premium 判断先行，Pro 200 会静默退回金卡。
    expect(PREMIUM_CODEX_PLAN_TYPES.has(ELITE_CODEX_PLAN_TYPE)).toBe(true);
    expect(resolvePlanTier('pro')).toBe('elite');
  });

  test('normalizes case and whitespace before matching', () => {
    expect(resolvePlanTier('PRO')).toBe('elite');
    expect(resolvePlanTier('  Pro  ')).toBe('elite');
    expect(resolvePlanTier('Pro-Lite')).toBe('premium');
  });

  test('maps every pro-lite spelling to premium', () => {
    expect(resolvePlanTier('prolite')).toBe('premium');
    expect(resolvePlanTier('pro-lite')).toBe('premium');
    expect(resolvePlanTier('pro_lite')).toBe('premium');
  });

  test('recognizes Business Premium without promoting other business entitlements', () => {
    expect(resolvePlanTier('self_serve_business_prolite')).toBe('premium');
    expect(resolvePlanTier('  SELF_SERVE_BUSINESS_PROLITE  ')).toBe('premium');
    expect(resolvePlanTier('self_serve_business_usage_based')).toBe('plain');
    expect(resolvePlanTier('self_serve_business_prolite_future')).toBe('plain');
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
