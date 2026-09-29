import { afterEach, expect, spyOn, test } from 'bun:test';
import { META_CONFIG } from '@/features/quota/providers/meta/data';
import { authFilesApi } from '@/services/api/authFiles';
import { apiCallApi } from '@/services/api/apiCall';
import { apiClient } from '@/services/api/client';
import { metaQuotaResponse } from './fixtures/metaQuota';
import { useQuotaStore } from '@/stores/useQuotaStore';
import i18n from '@/i18n';

for (const hasUsage of [true, false]) {
  test(`Meta adapter uses /api-call with ${hasUsage ? 'populated' : 'missing'} usage`, async () => {
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue(
      '{"dca_token":"dca:fixture-only","api_key":"LLM|unused"}'
    );
    const post = spyOn(apiClient, 'post').mockResolvedValue({
      status_code: 200,
      header: {},
      body: JSON.stringify({
        ...metaQuotaResponse,
        subs_usage: hasUsage ? metaQuotaResponse.subs_usage : undefined,
      }),
    });
    try {
      const data = await META_CONFIG.fetchQuota(file, i18n.t);
      expect(post).toHaveBeenCalledWith(
        '/api-call',
        {
          authIndex: 'fixture-index',
          method: 'POST',
          url: 'https://api.meta.ai/muse-code/key',
          header: {
            Accept: 'application/json',
            'Content-Type': 'application/json',
            Authorization: 'Bearer dca:fixture-only',
            'x-api-version': '1.0.0',
          },
          data: '{}',
        },
        undefined
      );
      const state = META_CONFIG.buildSuccessState(data);
      expect(state.status).toBe('success');
      expect(state.data?.windows.map((window) => window.usedPercent)).toEqual(
        hasUsage ? [2, 0] : [null, null]
      );
      for (const secret of ['dca:', 'LLM|', 'fixture@example.invalid', 'Fixture User']) {
        expect(JSON.stringify(state)).not.toContain(secret);
      }
    } finally {
      download.mockRestore();
      post.mockRestore();
    }
  });
}

const file = { name: 'meta-fixture.json', type: 'meta', authIndex: 'fixture-index' };
afterEach(() => useQuotaStore.getState().clearQuotaCache());

for (const invalidation of ['session', 'file'] as const) {
  test(`Meta adapter blocks a DCA request after ${invalidation} invalidation`, async () => {
    const download = spyOn(authFilesApi, 'downloadText').mockImplementation(async () => {
      useQuotaStore.getState().clearQuotaCache(invalidation === 'file' ? [file.name] : undefined);
      return '{"dca_token":"dca:fixture-only"}';
    });
    const request = spyOn(apiCallApi, 'request');
    try {
      await expect(META_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
        i18n.t('meta_quota.stale_request')
      );
      expect(download).toHaveBeenCalledWith(file.name);
      expect(request).not.toHaveBeenCalled();
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });
}

test('Meta adapter retains only sanitized quota data and tolerates unrelated file invalidation', async () => {
  const download = spyOn(authFilesApi, 'downloadText').mockImplementation(async () => {
    useQuotaStore.getState().clearQuotaCache(['unrelated.json']);
    return '{"dca_token":"dca:fixture-only"}';
  });
  const request = spyOn(apiCallApi, 'request').mockResolvedValue({
    statusCode: 200,
    header: {},
    bodyText: '',
    body: { api_key: 'LLM|fixture-secret', subs_usage: { weekly: { used_percent: 1 } } },
  });
  try {
    const data = await META_CONFIG.fetchQuota(file, i18n.t);
    expect(request.mock.calls[0][0].header?.Authorization).toBe('Bearer dca:fixture-only');
    const state = META_CONFIG.buildSuccessState(data);
    expect(state.data?.windows[1].usedPercent).toBe(1);
    expect(JSON.stringify(state)).not.toContain('fixture');
  } finally {
    download.mockRestore();
    request.mockRestore();
  }
});
