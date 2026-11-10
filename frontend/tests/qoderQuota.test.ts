import { describe, expect, test } from 'bun:test';
import { QODER_CONFIG, parseQoderPlan, parseQoderQuota } from '@/features/quota/providers/qoder/data';
import { classifyQuotaFiles } from '@/features/quota/logic';
import { collectQuotaRowInstants } from '@/features/quota/resetSchedule';
import { resolveAuthFileQuotaType } from '@/features/authFiles/logic';
import { getAuthFileIcon, getTypeColor } from '@/features/authFiles/constants';
import { useQuotaStore } from '@/stores/useQuotaStore';

const now = Date.parse('2026-11-09T00:00:00Z');
const reset = Date.parse('2026-12-01T00:00:00Z');

describe('Qoder subscription quotas', () => {
  test('keeps plan, add-on and organization buckets as separate rows', () => {
    const quota = parseQoderQuota(
      {
        userId: 'u1',
        userType: 'personal_standard',
        expiresAt: reset,
        userQuota: { total: 300, used: 139, remaining: 161, unit: 'credits' },
        addOnQuota: { total: 400, used: 0, remaining: 400 },
        orgResourcePackage: { cap: 500, used: 100, available: true },
      },
      now
    );
    expect(quota.rows.map((row) => row.labelKey)).toEqual([
      'qoder_quota.plan_credits',
      'qoder_quota.add_on_credits',
      'qoder_quota.org_credits',
    ]);
    expect(quota.rows[0]).toMatchObject({ used: 139, limit: 300, resetAtMs: reset });
    expect(quota.rows[2].limit).toBe(500);
    expect(collectQuotaRowInstants('qoder', QODER_CONFIG.buildSuccessState(quota))).toHaveLength(1);
  });

  test('accepts snake_case, skips unavailable or empty buckets and rejects garbage', () => {
    const quota = parseQoderQuota({
      user_quota: { total: 0, used: 0 },
      add_on_quota: { total: 50, used: 10 },
      org_resource_package: { cap: 100, used: 0, available: false },
    });
    expect(quota.rows).toHaveLength(1);
    expect(quota.rows[0].labelKey).toBe('qoder_quota.add_on_credits');
    expect(() => parseQoderQuota('<html>error</html>')).toThrow();
    expect(() => parseQoderQuota({ code: 'TOKEN_INVALID' })).toThrow();
    expect(parseQoderPlan({ plan_tier_name: 'Pro', user_type: 'personal' })).toBe('Pro');
  });

  test('both editions share the adapter, icon and badge', () => {
    expect(
      classifyQuotaFiles([
        { name: 'qoder-1.json', type: 'qoder' },
        { name: 'qoder-cn-1.json', type: 'qoder-cn' },
        { name: 'qoder-cn-2.json', type: 'qoder-cn', disabled: true },
      ]).map((entry) => entry.type)
    ).toEqual(['qoder', 'qoder']);
    expect(resolveAuthFileQuotaType({ name: 'qoder-cn-1.json', type: 'qoder-cn' }, 'qoder')).toBe(
      'qoder'
    );
    expect(getAuthFileIcon('qoder-cn', 'dark')).toBe(getAuthFileIcon('qoder', 'dark'));
    expect(getAuthFileIcon('qoder', 'light')).not.toBe(getAuthFileIcon('qoder', 'dark'));
    expect(getTypeColor('qoder-cn', 'light')).toEqual(getTypeColor('qoder', 'light'));
  });

  test('cache invalidation clears one Qoder credential only', () => {
    const store = useQuotaStore.getState();
    store.clearQuotaCache();
    store.setQoderQuota({
      'qoder-a.json': { status: 'success', rows: [] },
      'qoder-b.json': { status: 'success', rows: [] },
    });
    store.clearQuotaCache(['qoder-a.json']);
    expect(useQuotaStore.getState().qoderQuota['qoder-a.json']).toBeUndefined();
    expect(useQuotaStore.getState().qoderQuota['qoder-b.json']?.status).toBe('success');
    store.clearQuotaCache();
  });
});
