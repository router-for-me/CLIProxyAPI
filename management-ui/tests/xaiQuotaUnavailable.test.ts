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
  formatInstantShort,
  formatQuotaResetTime,
  mergeXaiBillingSummaries,
  resolveXaiSubscriptionPlan,
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

describe('resolveXaiSubscriptionPlan', () => {
  test('uses the settings display name and treats Heavy as the elite badge', () => {
    expect(resolveXaiSubscriptionPlan('SuperGrokPro', 'SuperGrok Heavy')).toEqual({
      label: 'SuperGrok Heavy',
      tier: 'elite',
    });
  });

  test('treats X Premium+ as the premium badge', () => {
    expect(resolveXaiSubscriptionPlan('XPremiumPlus', 'X Premium+')).toEqual({
      label: 'X Premium+',
      tier: 'premium',
    });
  });

  test('returns null when neither call produced a name', () => {
    expect(resolveXaiSubscriptionPlan(null, '  ')).toBeNull();
  });
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

  test('renders zero usage as 100% remaining', () => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: 0 }), monthlyConfig()));

    expect(markup).toContain('class="quotaPercent">100%</span>');
    expect(markup).not.toContain('Used 0%');
    expect(markup).not.toContain('Usage unavailable from xAI');
  });

  test('renders 37% usage as 63% remaining', () => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: 37 }), monthlyConfig()));

    expect(markup).toContain('class="quotaPercent">63%</span>');
    expect(markup).toContain('width:63%');
    expect(markup).not.toContain('Used 37%');
    expect(markup).not.toContain('Usage unavailable from xAI');
  });

  test.each([100, 120])('renders exhausted usage %i as 0% remaining', (percent) => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: percent }), monthlyConfig()));

    expect(markup).toContain('class="quotaPercent">0%</span>');
    expect(markup).toContain('width:0%');
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
    expect(render(quota)).toContain(`class="quotaPercent">${100 - percent}%</span>`);
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

  test('shows the Grok subscription name and the weekly reset beside it', () => {
    const quota = quotaFor(
      weeklyConfig({
        creditUsagePercent: 94,
        prepaidBalance: { val: 250 },
        productUsage: [
          { product: 'GrokBuild', usagePercent: 93 },
          { product: 'GrokChat', usagePercent: 1 },
        ],
      }),
      monthlyConfig({ used: { val: 4 } })
    );
    if (!quota.billing) throw new Error('missing billing');
    quota.billing.planLabel = 'SuperGrok Heavy';
    quota.billing.planTier = 'elite';

    const markup = render(quota);

    expect(markup).toContain('elitePlanValue');
    expect(markup).toContain('SuperGrok Heavy');
    expect(markup).toContain('Resets');
    expect(markup).toContain(formatInstantShort(Date.parse(WEEKLY_PERIOD_END)));
    expect(markup).toContain('GrokChat usage');
    expect(markup).toContain('Prepaid');
    expect(markup).toContain('$2.50');
    expect(markup).toContain('$0.00 / $0.00');
  });

  test('renders product contributions as text with only one shared weekly meter', () => {
    const markup = render(
      quotaFor(
        weeklyConfig({
          creditUsagePercent: 16,
          productUsage: [
            { product: 'GrokBuild', usagePercent: 14 },
            { product: 'GrokChat', usagePercent: 2 },
          ],
        }),
        monthlyConfig()
      )
    );

    expect(markup).toContain('class="quotaPercent">84%</span>');
    expect(markup).toContain('Usage breakdown (shared quota)');
    expect(markup).toContain('GrokBuild usage: 14%');
    expect(markup).toContain('GrokChat usage: 2%');
    expect(markup).toContain('width:84%');
    expect(markup.match(/class="quotaRow"/g)).toHaveLength(1);
    expect(markup).not.toContain('width:86%');
    expect(markup).not.toContain('width:98%');
  });

  test('does not derive a weekly total or product remainder from partial usage', () => {
    const markup = render(
      quotaFor(
        weeklyConfig({
          productUsage: [
            { product: 'GrokBuild', usagePercent: 14 },
            { product: 'GrokChat', usagePercent: null },
          ],
        }),
        monthlyConfig()
      )
    );

    expect(markup).toContain('GrokBuild usage: 14%');
    expect(markup).toContain('GrokChat usage: Usage unavailable from xAI');
    expect(markup).not.toContain('width:');
    expect(markup).not.toContain('Used 14%');
  });

  test('keeps monthly amount, percentage, and meter in the remaining direction', () => {
    const markup = render(
      quotaFor(
        null,
        monthlyConfig({
          monthlyLimit: { val: 15000 },
          used: { val: 1500 },
        })
      )
    );
    expect(markup).toContain('>90%<');
    expect(markup).toContain('$135.00 / $150.00');
    expect(markup).not.toContain('$15.00 / $150.00');
    expect(markup).toContain('width:90%');
  });

  test('places disabled pay-as-you-go in the plan row above weekly quota', () => {
    const markup = render(quotaFor(weeklyConfig({ creditUsagePercent: 37 }), monthlyConfig()));
    const label = i18n.t('xai_quota.pay_as_you_go_label');
    const disabled = i18n.t('xai_quota.pay_as_you_go_disabled');

    expect(markup).toContain(
      `<span class="codexPlanItem"><span class="codexPlanLabel">${label}</span><span class="codexPlanValue">${disabled}</span></span>`
    );
    expect(markup.indexOf(label)).toBeLessThan(markup.indexOf('class="quotaRow"'));
    expect(markup.split(label)).toHaveLength(2);
  });

  test('keeps enabled pay-as-you-go below weekly quota with its meter', () => {
    const markup = render(
      quotaFor(
        weeklyConfig({ creditUsagePercent: 37, onDemandCap: { val: 10000 } }),
        monthlyConfig({ onDemandCap: { val: 10000 }, onDemandUsed: { val: 2500 } })
      )
    );
    const label = i18n.t('xai_quota.pay_as_you_go_label');

    expect(markup).toContain(`<span class="quotaModel">${label}</span>`);
    expect(markup.indexOf(label)).toBeGreaterThan(markup.indexOf('class="quotaRow"'));
    expect(markup).not.toContain(i18n.t('xai_quota.pay_as_you_go_disabled'));
    expect(markup).toContain('width:75%');
    expect(markup.split(label)).toHaveLength(2);
  });

  test('keeps the monthly-only zero row when no weekly data exists', () => {
    const markup = render(quotaFor(null, monthlyConfig()));

    expect(markup).toContain('Monthly credits');
  });
});
