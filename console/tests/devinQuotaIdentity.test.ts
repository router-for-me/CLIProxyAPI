import { afterEach, describe, expect, test } from 'bun:test';
import { normalizeAuthFilesResponse } from '@/services/api/authFiles';
import { useQuotaStore } from '@/stores/useQuotaStore';
import type { AuthFileItem, DevinQuotaState } from '@/types';
import {
  getQuotaCacheFileName,
  getQuotaCacheKey,
  getQuotaDisplayName,
} from '@/utils/quota/identity';

const quotaState = (remainingPercent: number): DevinQuotaState => ({
  status: 'success',
  windows: [
    {
      id: 'daily',
      remainingPercent,
      resetAtMs: null,
      periodHours: 24,
    },
  ],
  observedAtMs: null,
  plan: null,
  planStartMs: null,
  planEndMs: null,
});

const devinFile = (authIndex: string | number | null): AuthFileItem => ({
  name: 'shared.json',
  provider: 'devin',
  authIndex,
});

afterEach(() => useQuotaStore.getState().clearQuotaCache());

describe('Devin quota identity', () => {
  test('uses normalized auth_index only for Devin cache identities', () => {
    expect(getQuotaCacheKey(devinFile(' 42 '))).toBe('shared.json\0' + '42');
    expect(getQuotaCacheKey(devinFile(42))).toBe('shared.json\0' + '42');
    expect(getQuotaCacheKey(devinFile(null))).toBe('shared.json\0');
    expect(getQuotaCacheFileName('shared.json\0' + '42')).toBe('shared.json');

    const codex = { ...devinFile('42'), provider: 'codex' };
    expect(getQuotaCacheKey(codex)).toBe('shared.json');
  });

  test('disambiguates displayed identities without using account secrets', () => {
    expect(getQuotaDisplayName({ ...devinFile('42'), email: 'demo@example.test' })).toBe(
      'shared.json · demo@example.test'
    );
    expect(getQuotaDisplayName({ ...devinFile('42'), account: 'secret' })).toBe('shared.json · 42');
    expect(getQuotaDisplayName({ ...devinFile(null), account: 'secret' })).toBe('shared.json');
  });

  test('does not merge same-name Devin identities or leak quota and email between them', () => {
    const response = normalizeAuthFilesResponse({
      files: [
        {
          name: 'shared.json',
          provider: 'devin',
          auth_index: '2',
          email: 'second@example.test',
          quota: { signals: { daily_quota_remaining_percent: 20 } },
        },
        {
          name: 'shared.json',
          provider: 'devin',
          auth_index: 1,
          email: 'first@example.test',
          quota: { signals: { daily_quota_remaining_percent: 80 } },
        },
      ],
    });

    expect(response.files).toHaveLength(2);
    expect(response.files.map((file) => [file.authIndex, file.email, file.quota])).toEqual([
      ['1', 'first@example.test', { signals: { daily_quota_remaining_percent: 80 } }],
      ['2', 'second@example.test', { signals: { daily_quota_remaining_percent: 20 } }],
    ]);
  });

  test('retains filename deduplication for providers other than Devin', () => {
    const response = normalizeAuthFilesResponse({
      files: [
        { name: 'shared.json', provider: 'codex', auth_index: '1', email: 'first@example.test' },
        { name: 'shared.json', provider: 'codex', auth_index: '2', quota: { remaining: 25 } },
      ],
    });

    expect(response.files).toHaveLength(1);
    expect(response.files[0]?.name).toBe('shared.json');
    expect(response.files[0]?.quota).toEqual({ remaining: 25 });
  });

  test('stores identities independently and filename invalidation clears both only', () => {
    const firstKey = getQuotaCacheKey(devinFile('first'));
    const secondKey = getQuotaCacheKey(devinFile('second'));
    const otherKey = getQuotaCacheKey({
      name: 'other.json',
      provider: 'devin',
      authIndex: 'first',
    });
    const first = quotaState(80);
    const second = quotaState(20);
    const other = quotaState(60);

    useQuotaStore.getState().setDevinQuota({
      [firstKey]: first,
      [secondKey]: second,
      [otherKey]: other,
    });

    expect(useQuotaStore.getState().devinQuota[firstKey]).toBe(first);
    expect(useQuotaStore.getState().devinQuota[secondKey]).toBe(second);

    useQuotaStore.getState().clearQuotaCache(['shared.json']);
    expect(useQuotaStore.getState().devinQuota[firstKey]).toBeUndefined();
    expect(useQuotaStore.getState().devinQuota[secondKey]).toBeUndefined();
    expect(useQuotaStore.getState().devinQuota[otherKey]).toBe(other);
    expect(useQuotaStore.getState().fileGenerations['shared.json']).toBe(1);
  });
});
