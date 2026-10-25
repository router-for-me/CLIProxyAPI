import { describe, expect, test } from 'bun:test';
import { KIRO_CONFIG, parseKiroQuota } from '@/features/quota/providers/kiro/data';
import { classifyQuotaFiles } from '@/features/quota/logic';
import { collectExpiringQuotaRows } from '@/features/quota/expiringQuota';
import { collectQuotaRowInstants } from '@/features/quota/resetSchedule';
import { useQuotaStore } from '@/stores/useQuotaStore';

const now = Date.parse('2026-10-24T00:00:00Z');
const reset = Date.parse('2026-11-01T00:00:00Z');

describe('Kiro subscription quotas', () => {
  test('keeps monthly, trial and bonus credit windows separate', () => {
    const quota = parseKiroQuota(
      {
        nextDateReset: reset / 1000,
        subscriptionInfo: { subscriptionTitle: 'Kiro Pro' },
        usageBreakdownList: [
          {
            resourceType: 'CREDIT',
            currentUsage: 21,
            currentUsageWithPrecision: 20.5,
            usageLimit: 100,
            freeTrialInfo: {
              freeTrialStatus: 'ACTIVE',
              currentUsage: 3,
              usageLimit: 50,
              freeTrialExpiry: reset / 1000,
            },
            bonuses: [
              {
                status: 'ACTIVE',
                currentUsage: 0,
                usageLimit: 10,
                expiresAt: '2026-11-03T00:00:00Z',
              },
              { status: 'EXPIRED', currentUsage: 0, usageLimit: 100, expiresAt: reset / 1000 },
            ],
          },
        ],
      },
      now
    );
    expect(quota.plan).toBe('Kiro Pro');
    expect(quota.rows).toHaveLength(3);
    expect(quota.rows[0]).toMatchObject({
      used: 20.5,
      limit: 100,
      resetAtMs: reset,
      periodHours: 720,
    });
    expect(quota.rows[1].labelKey).toBe('kiro_quota.trial_credits');
    expect(quota.rows[2].limit).toBe(10);
    const state = KIRO_CONFIG.buildSuccessState(quota);
    expect(collectQuotaRowInstants('kiro', state)).toHaveLength(3);
    expect(collectExpiringQuotaRows('kiro', state, now)).toHaveLength(3);
  });

  test('does not turn missing counters into zero usage', () => {
    const quota = parseKiroQuota(
      { usageBreakdownList: [{ usageLimit: 100 }, { currentUsage: 0, usageLimit: 100 }] },
      now
    );
    expect(quota.rows).toHaveLength(1);
    expect(quota.rows[0].used).toBe(0);
    expect(quota.rows[0].resetAtMs).toBeNull();
    expect(() => parseKiroQuota('<html>error</html>', now)).toThrow();
  });

  test('classifies Kiro credentials and honors disabled state', () => {
    expect(
      classifyQuotaFiles([
        { name: 'kiro-1.json', type: 'kiro' },
        { name: 'kiro-2.json', type: 'kiro', disabled: true },
      ]).map((entry) => entry.type)
    ).toEqual(['kiro']);
  });

  test('credential cache invalidation clears Kiro without clearing another file', () => {
    const store = useQuotaStore.getState();
    store.clearQuotaCache();
    store.setKiroQuota({
      'kiro-a.json': { status: 'success', rows: [] },
      'kiro-b.json': { status: 'success', rows: [] },
    });
    store.clearQuotaCache(['kiro-a.json']);
    expect(useQuotaStore.getState().kiroQuota['kiro-a.json']).toBeUndefined();
    expect(useQuotaStore.getState().kiroQuota['kiro-b.json']?.status).toBe('success');
    store.clearQuotaCache();
  });
});
