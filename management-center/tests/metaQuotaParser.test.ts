import { describe, expect, test } from 'bun:test';
import { parseMetaQuotaPayload } from '@/services/api/metaQuota';
import { metaQuotaResponse } from './fixtures/metaQuota';

describe('Meta Muse quota parser', () => {
  test('parses the key endpoint response and retains only subscription and quota fields', () => {
    expect(parseMetaQuotaPayload(metaQuotaResponse)).toEqual({
      planName: 'Muse Code Everyday Usage',
      isSubscriptionActive: true,
      windows: [
        { id: 'window', usedPercent: 2, resetAt: 1789678120, durationMinutes: 300 },
        { id: 'weekly', usedPercent: 0, resetAt: 1789948800 },
      ],
    });
  });

  test('falls back to usage tier and parses a JSON string', () => {
    const quota = parseMetaQuotaPayload(
      JSON.stringify({ subs_usage: { tier: ' fixture-tier ', weekly: { used_percent: '25' } } })
    );
    expect(quota?.planName).toBe('fixture-tier');
    expect(quota?.windows).toEqual([
      { id: 'window', usedPercent: null },
      { id: 'weekly', usedPercent: 25 },
    ]);
  });

  test('clamps finite percentages and rejects invalid reset/duration values', () => {
    const quota = parseMetaQuotaPayload({
      subs_usage: {
        window: { used_percent: -4, resets_at: 0, window_duration_mins: 'bad' },
        weekly: { used_percent: 120, resets_at: Number.NaN },
      },
    });
    expect(quota?.windows).toEqual([
      { id: 'window', usedPercent: 0 },
      { id: 'weekly', usedPercent: 100 },
    ]);
  });

  test('missing usage is unknown even without subscription metadata', () => {
    for (const payload of [
      {},
      { api_key: 'LLM|fixture-only' },
      { subs_usage: null },
      { subs_usage: {} },
      { subs_usage: { window: null, weekly: {} } },
      { ...metaQuotaResponse, subs_usage: undefined },
    ]) {
      expect(parseMetaQuotaPayload(payload)?.windows).toEqual([
        { id: 'window', usedPercent: null },
        { id: 'weekly', usedPercent: null },
      ]);
    }
  });

  test('never turns invalid percentages into zero', () => {
    for (const used of [undefined, null, '', ' ', 'bad', false, [], {}, NaN, Infinity]) {
      const quota = parseMetaQuotaPayload({
        subs_usage: { window: { used_percent: used }, weekly: { used_percent: used } },
      });
      expect(quota?.windows.map((window) => window.usedPercent)).toEqual([null, null]);
    }
  });

  test('distinguishes malformed bodies from valid objects without usage', () => {
    for (const payload of ['{not-json', '', 'null', '[]', '42', null, undefined, [], 42]) {
      expect(parseMetaQuotaPayload(payload)).toBeNull();
    }
  });

  test('preserves false subscription status without inventing quota', () => {
    expect(parseMetaQuotaPayload({ is_subs_active: false })).toEqual({
      isSubscriptionActive: false,
      windows: [
        { id: 'window', usedPercent: null },
        { id: 'weekly', usedPercent: null },
      ],
    });
  });
});
