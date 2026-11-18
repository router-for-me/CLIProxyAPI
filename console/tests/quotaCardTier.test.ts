import { describe, expect, test } from 'bun:test';
import {
  XAI_SUPERGROK_HEAVY_LIMIT_CENTS,
  planValueClassForTier,
  resolveAntigravityCardTier,
  resolveClaudeCardTier,
  resolveCodexCardTier,
  resolveKimiCardTier,
  resolveXaiCardTier,
} from '@/features/quota/cardTier';
import { QUOTA_CLASS_KEYS, bindQuotaClasses } from '@/features/quota/types';
import type { XaiBillingSummary } from '@/types';

const classes = bindQuotaClasses(
  Object.fromEntries(QUOTA_CLASS_KEYS.map((key) => [key, key])),
  'test-host'
);

const billing = (overrides: Partial<XaiBillingSummary> = {}): XaiBillingSummary => ({
  mode: 'billing',
  periodType: 'monthly',
  usagePercent: null,
  productUsage: [],
  monthlyLimitCents: null,
  usedCents: null,
  includedUsedCents: null,
  onDemandCapCents: null,
  onDemandUsedCents: null,
  onDemandUsedPercent: null,
  usedPercent: null,
  ...overrides,
});

describe('quota plan tier ladder', () => {
  test('Codex: Pro 500 is flagship, Pro 200 and Pro 100 are premium, Business ranks with Plus', () => {
    expect(resolveCodexCardTier('promax')).toBe('flagship');
    expect(resolveCodexCardTier('pro')).toBe('premium');
    expect(resolveCodexCardTier('prolite')).toBe('premium');
    expect(resolveCodexCardTier('self_serve_business_prolite')).toBe('standard');
    expect(resolveCodexCardTier('team')).toBe('standard');
    expect(resolveCodexCardTier('plus')).toBe('standard');
    expect(resolveCodexCardTier(null)).toBe('standard');
  });

  test.each([
    ['Allegro', 'promax'],
    ['Allegretto', 'pro'],
    ['Moderato', 'plus'],
  ])('Kimi %s shares the Codex %s tier', (planName, codexPlan) => {
    const expected = resolveCodexCardTier(codexPlan);
    expect(resolveKimiCardTier(planName)).toBe(expected);
    expect(resolveKimiCardTier(` ${planName.toUpperCase()} `)).toBe(expected);
  });

  test('Kimi Andante and unknown names stay standard', () => {
    for (const name of ['Andante', 'Vivace', 'Allegro Plus', '', null, undefined]) {
      expect(resolveKimiCardTier(name)).toBe('standard');
    }
  });

  test('Antigravity: Ultra is flagship, Ultra Lite is premium', () => {
    expect(resolveAntigravityCardTier('ultra')).toBe('flagship');
    expect(resolveAntigravityCardTier(' Ultra-Lite ')).toBe('premium');
    expect(resolveAntigravityCardTier('pro')).toBe('standard');
    expect(resolveAntigravityCardTier(null)).toBe('standard');
  });

  test('Claude: Max 20x is flagship, Max 5x and an unspecified Max are premium', () => {
    expect(resolveClaudeCardTier('plan_max20')).toBe('flagship');
    expect(resolveClaudeCardTier('plan_max5')).toBe('premium');
    expect(resolveClaudeCardTier('plan_max')).toBe('premium');
    expect(resolveClaudeCardTier('plan_pro')).toBe('standard');
    expect(resolveClaudeCardTier(null)).toBe('standard');
  });

  test('xAI: subscription tier wins, then paid API, then the Heavy monthly limit', () => {
    expect(resolveXaiCardTier(billing({ planLabel: 'SuperGrok Heavy', planTier: 'elite' }))).toBe(
      'flagship'
    );
    expect(resolveXaiCardTier(billing({ planLabel: 'SuperGrok', planTier: 'premium' }))).toBe(
      'premium'
    );
    expect(resolveXaiCardTier(billing({ planLabel: 'Basic', planTier: 'standard' }))).toBe(
      'standard'
    );
    expect(resolveXaiCardTier(billing({ mode: 'paid-health' }))).toBe('premium');
    expect(
      resolveXaiCardTier(billing({ monthlyLimitCents: XAI_SUPERGROK_HEAVY_LIMIT_CENTS }))
    ).toBe('flagship');
    // The monthly-limit fallback agrees with the subscription interface.
    expect(resolveXaiCardTier(billing({ monthlyLimitCents: 15_000 }))).toBe('premium');
    expect(resolveXaiCardTier(billing({ monthlyLimitCents: 3_000 }))).toBe('standard');
  });

  test('the plan text class follows the tier', () => {
    expect(planValueClassForTier('flagship', classes)).toBe('elitePlanValue');
    expect(planValueClassForTier('premium', classes)).toBe('premiumPlanValue');
    expect(planValueClassForTier('standard', classes)).toBe('codexPlanValue');
  });
});
