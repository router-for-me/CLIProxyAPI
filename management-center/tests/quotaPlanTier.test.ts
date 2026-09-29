import { describe, expect, test } from 'bun:test';
import {
  ELITE_CODEX_PLAN_TYPE,
  PREMIUM_CODEX_PLAN_TYPES,
  resolvePlanTier,
} from '@/utils/quota';

describe('resolvePlanTier', () => {
  test("elite wins for 'pro' even though it is also in the premium set (order contract)", () => {
    // 顺序契约回归：'pro' 同时命中 PREMIUM_CODEX_PLAN_TYPES，
    // 一旦 premium 判断先行，Pro 20x 会静默退回金卡。
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
