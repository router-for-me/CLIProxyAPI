/**
 * xAI quota body: unknown usage must not masquerade as a zero.
 *
 * When the weekly endpoint omits creditUsagePercent the summary keeps
 * usagePercent null; the body should say the usage is unavailable and hide
 * the meter rather than render a fabricated "Used 0%". The legacy
 * zero-limit/zero-used monthly row is hidden only while weekly data exists.
 */

import { beforeAll, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { XaiQuotaBody } from '@/features/quota/providers/xai/XaiQuotaBody';
import { QUOTA_CLASS_KEYS, bindQuotaClasses } from '@/features/quota/types';
import {
  buildXaiBillingSummary,
  formatQuotaResetTime,
  mergeXaiBillingSummaries,
} from '@/utils/quota';
import type { XaiBillingConfig, XaiQuotaState } from '@/types';

const classes = bindQuotaClasses(
  Object.fromEntries(QUOTA_CLASS_KEYS.map((key) => [key, key])),
  'test-host'
);

const WEEKLY_PERIOD_START = '2026-09-17T13:32:42.093205+00:00';
const WEEKLY_PERIOD_END = '2026-09-24T13:32:42.093205+00:00';

const weeklyConfig = (extra: XaiBillingConfig = {}): XaiBillingConfig => ({
  currentPeriod: {
    type: 'USAGE_PERIOD_TYPE_WEEKLY',
    start: WEEKLY_PERIOD_START,
    end: WEEKLY_PERIOD_END,
  },
  billingPeriodStart: WEEKLY_PERIOD_START,
  billingPeriodEnd: WEEKLY_PERIOD_END,
  onDemandCap: { val: 0 },
  ...extra,
});

const monthlyConfig = (extra: XaiBillingConfig = {}): XaiBillingConfig => ({
  monthlyLimit: { val: 0 },
  used: { val: 0 },
  onDemandCap: { val: 0 },
  billingPeriodStart: '2026-09-01T00:00:00+00:00',
  billingPeriodEnd: '2026-10-01T00:00:00+00:00',
  ...extra,
});

const quotaFor = (
  weekly: XaiBillingConfig | null,
  monthly: XaiBillingConfig | null
): XaiQuotaState => ({
  status: 'success',
  billing: mergeXaiBillingSummaries(
    buildXaiBillingSummary(weekly),
    buildXaiBillingSummary(monthly)
  ),
});

const render = (quota: XaiQuotaState): string =>
  renderToStaticMarkup(createElement(XaiQuotaBody, { quota, classes }));

beforeAll(async () => {
  await i18n.changeLanguage('en');
});

describe('XaiQuotaBody unavailable weekly usage', () => {
  test('reports unavailable instead of a fabricated zero when usagePercent is missing', () => {
    const quota = quotaFor(weeklyConfig(), monthlyConfig());
    expect(quota.billing?.usagePercent).toBeNull();

    const markup = render(quota);

    expect(markup).toContain('Usage unavailable from xAI');
    expect(markup).toContain(formatQuotaResetTime(WEEKLY_PERIOD_END));
    expect(markup).not.toContain('Used --');
    expect(markup).not.toContain('Used 0%');
    // Zero-budget zero-used monthly row is hidden while weekly data exists.
    expect(markup).not.toContain('Monthly credits');
    expect(markup).not.toContain('$0.00 / $0.00');
  });

  test('keeps usagePercent null for a malformed percentage string', () => {
    const quota = quotaFor(weeklyConfig({ creditUsagePercent: 'not-a-number' }), monthlyConfig());
    expect(quota.billing?.usagePercent).toBeNull();

    const markup = render(quota);

    expect(markup).toContain('Usage unavailable from xAI');
    expect(markup).not.toContain('Used 0%');
  });

  test('renders an explicit zero percent as Used 0%', () => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: 0 }), monthlyConfig()));

    expect(markup).toContain('Used 0%');
    expect(markup).not.toContain('Usage unavailable from xAI');
  });

  test('renders an explicit percent as Used 37%', () => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: 37 }), monthlyConfig()));

    expect(markup).toContain('Used 37%');
    expect(markup).not.toContain('Usage unavailable from xAI');
  });

  test.each([0, 3000])('does not use monthly spending %i as weekly usage', (used) => {
    const quota = quotaFor(
      weeklyConfig(),
      monthlyConfig({ monthlyLimit: { val: 15000 }, used: { val: used } })
    );

    expect(quota.billing?.periodType).toBe('weekly');
    expect(quota.billing?.usagePercent).toBeNull();
    expect(quota.billing?.usedPercent).toBe((used / 15000) * 100);

    const markup = render(quota);
    expect(markup).toContain('Monthly credits');
    expect(markup).toContain('Usage unavailable from xAI');
    expect(markup).toContain(formatQuotaResetTime(WEEKLY_PERIOD_END));
  });

  test.each([0, 37])('preserves explicit weekly usage %i with monthly spending', (percent) => {
    const quota = quotaFor(
      weeklyConfig({ creditUsagePercent: percent }),
      monthlyConfig({ monthlyLimit: { val: 15000 }, used: { val: 3000 } })
    );

    expect(quota.billing?.usagePercent).toBe(percent);
    expect(quota.billing?.usedPercent).toBe(20);
    expect(render(quota)).toContain(`Used ${percent}%`);
  });

  test('preserves monthly-only usage', () => {
    const quota = quotaFor(
      null,
      monthlyConfig({ monthlyLimit: { val: 15000 }, used: { val: 3000 } })
    );

    expect(quota.billing?.periodType).toBe('monthly');
    expect(quota.billing?.usagePercent).toBe(20);
    expect(render(quota)).toContain('Monthly credits');
  });

  test('shows the monthly row when the limit is zero but usage is nonzero', () => {
    const markup = render(quotaFor(weeklyConfig(), monthlyConfig({ used: { val: 100 } })));

    expect(markup).toContain('Monthly credits');
  });

  test('keeps the monthly-only zero row when no weekly data exists', () => {
    const markup = render(quotaFor(null, monthlyConfig()));

    expect(markup).toContain('Monthly credits');
  });
});
