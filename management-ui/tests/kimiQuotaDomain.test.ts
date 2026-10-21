import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import i18n from '@/i18n';
import { KIMI_CONFIG } from '@/features/quota/providers/kimi/data';
import { apiCallApi, authFilesApi } from '@/services/api';
import { parseKimiQuotaUrl } from '@/services/api/kimiQuota';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { KIMI_AI_USAGE_URL, KIMI_USAGE_URL } from '@/utils/quota';

const file = { name: 'kimi-ai-fixture.json', provider: 'kimi-ai', authIndex: 'fixture-index' };
const response = { statusCode: 200, header: {}, bodyText: '', body: { limits: [] } };
afterEach(() => useQuotaStore.getState().clearQuotaCache());

describe('Kimi effective quota domain', () => {
  test.each([
    [{ type: 'kimi-ai', domain: 'kimi.com' }, KIMI_USAGE_URL],
    [{ type: 'kimi', domain: 'ai' }, KIMI_AI_USAGE_URL],
    [{ domain: ' COM ', base_url: 'https://api.kimi.ai/coding' }, KIMI_USAGE_URL],
    [{ domain: 'auth.kimi.ai', base_url: 'https://api.kimi.com/coding' }, KIMI_AI_USAGE_URL],
    [{ type: 'kimi-ai', base_url: 'https://api.kimi.com/coding' }, KIMI_USAGE_URL],
    [{ type: 'kimi', base_url: 'https://api.kimi.ai/coding' }, KIMI_AI_USAGE_URL],
    [{ type: 'kimi', 'base-url': 'https://api.kimi.ai/coding' }, KIMI_AI_USAGE_URL],
    [{ type: 'kimi-ai', 'base-url': 'https://api.kimi.com/coding' }, KIMI_USAGE_URL],
    [
      { type: 'kimi', base_url: 'https://api.kimi.com', 'base-url': 'https://api.kimi.ai' },
      KIMI_USAGE_URL,
    ],
    [{ type: 'kimi', base_url: null, 'base-url': 'https://api.kimi.ai' }, KIMI_USAGE_URL],
    [{ type: 'kimi', base_url: '', 'base-url': 'https://api.kimi.ai' }, KIMI_USAGE_URL],
    [{ type: 'kimi', base_url: 'https://kimi.ai.evil.invalid/coding' }, KIMI_USAGE_URL],
    [{ type: 'kimi', base_url: 'https://kimi.ai@evil.invalid/coding' }, KIMI_USAGE_URL],
    [{ type: 'kimi', base_url: 'not a URL' }, KIMI_USAGE_URL],
    [{ type: 'kimi', domain: ' ', base_url: 'https://api.kimi.ai/coding' }, KIMI_AI_USAGE_URL],
    // The file synthesizer normalizes an unrecognized explicit domain to kimi.com.
    [{ type: 'kimi-ai', domain: 'unknown', base_url: 'https://api.kimi.ai' }, KIMI_USAGE_URL],
    [{ type: 'kimi' }, KIMI_USAGE_URL],
    [{ type: 'kimi-ai' }, KIMI_AI_USAGE_URL],
  ])('resolves %j to an allowlisted endpoint', (credential, expected) => {
    expect(parseKimiQuotaUrl(JSON.stringify(credential), file)).toBe(expected);
  });

  test.each(['null', '[]', '"secret-fixture"', '{"access_token":"secret-fixture"'])(
    'rejects malformed/non-object credentials without echoing contents: %s',
    (text) => {
      expect(() => parseKimiQuotaUrl(text, file)).toThrow('Invalid Kimi credential');
    }
  );

  test('downloads the credential and honors its domain instead of its provider', async () => {
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue(
      JSON.stringify({ type: 'kimi-ai', domain: 'kimi.com', access_token: 'fixture-secret' })
    );
    const request = spyOn(apiCallApi, 'request').mockResolvedValue(response);
    try {
      const rows = await KIMI_CONFIG.fetchQuota(file, i18n.t);
      expect(download).toHaveBeenCalledWith(file.name);
      expect(request.mock.calls[0][0]).toMatchObject({
        authIndex: 'fixture-index',
        url: KIMI_USAGE_URL,
        header: { Authorization: 'Bearer $TOKEN$' },
      });
      expect(JSON.stringify(request.mock.calls)).not.toContain('fixture-secret');
      expect(JSON.stringify(KIMI_CONFIG.buildSuccessState(rows))).not.toContain('fixture-secret');
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });

  test.each(['download', 'json'])('does not guess a host after a %s failure', async (failure) => {
    const download = spyOn(authFilesApi, 'downloadText').mockImplementation(async () => {
      if (failure === 'download') throw new Error('fixture-secret');
      return '{"access_token":"fixture-secret"';
    });
    const request = spyOn(apiCallApi, 'request');
    try {
      await expect(KIMI_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
        i18n.t('kimi_quota.domain_unavailable')
      );
      expect(request).not.toHaveBeenCalled();
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });

  test.each([{ runtimeOnly: true }, { runtime_only: 'true' }, { name: '' }])(
    'rejects credentials that cannot be downloaded: %j',
    async (fields) => {
      const download = spyOn(authFilesApi, 'downloadText');
      const request = spyOn(apiCallApi, 'request');
      try {
        await expect(KIMI_CONFIG.fetchQuota({ ...file, ...fields }, i18n.t)).rejects.toThrow(
          i18n.t('kimi_quota.domain_unavailable')
        );
        expect(download).not.toHaveBeenCalled();
        expect(request).not.toHaveBeenCalled();
      } finally {
        download.mockRestore();
        request.mockRestore();
      }
    }
  );

  for (const scope of ['session', 'file', 'unrelated'] as const) {
    test(`guards ${scope} invalidation while downloading`, async () => {
      const download = spyOn(authFilesApi, 'downloadText').mockImplementation(async () => {
        useQuotaStore
          .getState()
          .clearQuotaCache(
            scope === 'session' ? undefined : [scope === 'file' ? file.name : 'unrelated.json']
          );
        return '{"type":"kimi-ai","domain":"kimi.com"}';
      });
      const request = spyOn(apiCallApi, 'request').mockResolvedValue(response);
      try {
        if (scope === 'unrelated') {
          await KIMI_CONFIG.fetchQuota(file, i18n.t);
          expect(request).toHaveBeenCalledTimes(1);
        } else {
          await expect(KIMI_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
            i18n.t('kimi_quota.stale_request')
          );
          expect(request).not.toHaveBeenCalled();
        }
      } finally {
        download.mockRestore();
        request.mockRestore();
      }
    });
  }

  test('reads the domain again on each refresh instead of caching credentials', async () => {
    const download = spyOn(authFilesApi, 'downloadText')
      .mockResolvedValueOnce('{"type":"kimi-ai","domain":"kimi.com"}')
      .mockResolvedValueOnce('{"type":"kimi-ai","domain":"kimi.ai"}');
    const request = spyOn(apiCallApi, 'request').mockResolvedValue(response);
    try {
      await KIMI_CONFIG.fetchQuota(file, i18n.t);
      await KIMI_CONFIG.fetchQuota(file, i18n.t);
      expect(download).toHaveBeenCalledTimes(2);
      expect(request.mock.calls.map(([payload]) => payload.url)).toEqual([
        KIMI_USAGE_URL,
        KIMI_AI_USAGE_URL,
      ]);
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });

  test('rejects a quota response after session invalidation', async () => {
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue('{"type":"kimi-ai"}');
    const request = spyOn(apiCallApi, 'request').mockImplementation(async () => {
      useQuotaStore.getState().clearQuotaCache();
      return response;
    });
    try {
      await expect(KIMI_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
        i18n.t('kimi_quota.stale_request')
      );
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });
});
