import { describe, expect, test } from 'bun:test';
import { createMetaQuotaFetcher, MetaQuotaError } from '@/features/quota/providers/meta/requests';
import type { ApiCallRequest, ApiCallResult } from '@/services/api/apiCall';
import type { AuthFileItem } from '@/types';

const file = (authIndex: string | number | undefined): AuthFileItem => ({
  name: 'meta.json',
  provider: 'meta',
  authIndex,
});
const result = (statusCode: number, body: unknown): ApiCallResult => ({
  statusCode,
  header: {},
  bodyText: JSON.stringify(body),
  body,
});
const defaults = {
  downloadText: async () =>
    JSON.stringify({ dca_token: 'dca:fixture-only', api_key: 'LLM|unused' }),
  captureCurrent: () => () => true,
  request: async () => result(200, { subs_usage: { weekly: { used_percent: 1 } } }),
};
const expectMetaError = async (promise: Promise<unknown>, code: string, status?: number) => {
  try {
    await promise;
    throw new Error('expected request to reject');
  } catch (error) {
    expect(error).toBeInstanceOf(MetaQuotaError);
    expect((error as MetaQuotaError).code).toBe(code);
    expect((error as MetaQuotaError).status).toBe(status);
    return error as MetaQuotaError;
  }
};

describe('Meta Muse DCA quota request', () => {
  test('downloads the named auth file and sends its DCA through api-call, not $TOKEN$', async () => {
    const requests: ApiCallRequest[] = [];
    const names: string[] = [];
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      downloadText: async (name) => {
        names.push(name);
        return JSON.stringify({ dca_token: ' dca:fixture-only ', api_key: 'LLM|unused' });
      },
      request: async (request) => {
        requests.push(request);
        return result(200, {
          subs_usage: {
            window: { used_percent: 0, window_duration_mins: 300 },
            weekly: { used_percent: 1 },
          },
          api_key: 'must-not-propagate',
          user_email: 'fixture@example.invalid',
        });
      },
    });
    const quota = await fetchQuota(file(' 007 '));
    expect(names).toEqual(['meta.json']);
    expect(requests).toEqual([
      {
        authIndex: '007',
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
    ]);
    for (const secret of ['dca:', 'LLM|', 'must-not-propagate', 'fixture@example.invalid']) {
      expect(JSON.stringify(quota)).not.toContain(secret);
    }
  });

  test('rejects missing identity and runtime-only files before downloading', async () => {
    let calls = 0;
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      downloadText: async () => {
        calls++;
        return '{}';
      },
    });
    await expectMetaError(fetchQuota(file(undefined)), 'missing_auth_index');
    await expectMetaError(fetchQuota({ ...file('1'), name: '' }), 'missing_file');
    await expectMetaError(fetchQuota({ ...file('1'), runtimeOnly: true }), 'missing_file');
    await expectMetaError(fetchQuota({ ...file('1'), runtime_only: 'true' }), 'missing_file');
    expect(calls).toBe(0);
  });

  test('rejects malformed files and missing DCA without falling back to another token', async () => {
    let calls = 0;
    for (const [text, code] of [
      ['{secret', 'invalid_auth_file'],
      ['null', 'invalid_auth_file'],
      ['[]', 'invalid_auth_file'],
      ['{"api_key":"LLM|fixture","access_token":"dca:unused"}', 'missing_dca_token'],
      ['{"dca_token":"LLM|wrong"}', 'missing_dca_token'],
      ['{"dca_token":"dca:"}', 'missing_dca_token'],
      ['{"dca_token":"dca:bad\\r\\nheader"}', 'missing_dca_token'],
    ]) {
      const fetchQuota = createMetaQuotaFetcher({
        ...defaults,
        downloadText: async () => text,
        request: async () => {
          calls++;
          return result(200, {});
        },
      });
      const error = await expectMetaError(fetchQuota(file('1')), code);
      expect(JSON.stringify(error)).not.toContain(text);
    }
    expect(calls).toBe(0);
  });

  test('redacts download failures', async () => {
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      downloadText: async () => {
        throw new Error('dca:secret');
      },
    });
    const error = await expectMetaError(fetchQuota(file('1')), 'download_failed');
    expect(JSON.stringify(error)).not.toContain('dca:secret');
  });

  test('blocks sending the DCA after a connection or credential change during download', async () => {
    let current = true;
    let calls = 0;
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      captureCurrent: () => () => current,
      downloadText: async () => {
        current = false;
        return defaults.downloadText();
      },
      request: async () => {
        calls++;
        return result(200, {});
      },
    });
    await expectMetaError(fetchQuota(file('1')), 'stale_request');
    expect(calls).toBe(0);
  });

  test('discards a quota response after a connection or credential change', async () => {
    let current = true;
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      captureCurrent: () => () => current,
      request: async () => {
        current = false;
        return defaults.request();
      },
    });
    await expectMetaError(fetchQuota(file('1')), 'stale_request');
  });

  test('does not reuse DCA across refreshes', async () => {
    let downloads = 0;
    const headers: string[] = [];
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      downloadText: async () => JSON.stringify({ dca_token: `dca:fixture-${++downloads}` }),
      request: async (payload) => {
        headers.push(payload.header!.Authorization);
        return defaults.request();
      },
    });
    await fetchQuota(file('1'));
    await fetchQuota(file('1'));
    expect(headers).toEqual(['Bearer dca:fixture-1', 'Bearer dca:fixture-2']);
  });

  test('redacts upstream errors and management exceptions including request headers', async () => {
    for (const throws of [false, true]) {
      const fetchQuota = createMetaQuotaFetcher({
        ...defaults,
        request: async () => {
          if (throws)
            throw Object.assign(new Error('dca:secret'), {
              status: 429,
              config: { Authorization: 'dca:secret' },
            });
          return result(429, { error: 'dca:secret', api_key: 'LLM|secret' });
        },
      });
      const error = await expectMetaError(fetchQuota(file('1')), 'request_failed', 429);
      expect(error.message).toBe('request_failed');
      expect(JSON.stringify(error)).not.toContain('secret');
    }
  });

  test('returns unknown windows for successful responses without quota fields', async () => {
    for (const body of [{}, { api_key: 'fixture-only' }, { subs_usage: null }]) {
      const fetchQuota = createMetaQuotaFetcher({
        ...defaults,
        request: async () => result(200, body),
      });
      expect(await fetchQuota(file('1'))).toEqual({
        windows: [
          { id: 'window', usedPercent: null },
          { id: 'weekly', usedPercent: null },
        ],
      });
    }
  });

  test('keeps malformed responses distinct from unknown quota without exposing the body', async () => {
    for (const body of ['<html>fixture-secret</html>', '{bad-json', null, [], 42]) {
      const fetchQuota = createMetaQuotaFetcher({
        ...defaults,
        request: async () => result(200, body),
      });
      const error = await expectMetaError(fetchQuota(file('1')), 'invalid_response');
      expect(JSON.stringify(error)).not.toContain('fixture-secret');
    }
  });

  test('does not treat unsuccessful or invalid status codes as unknown quota', async () => {
    for (const status of [0, NaN, 401, 403, 429, 500]) {
      const fetchQuota = createMetaQuotaFetcher({
        ...defaults,
        request: async () => result(status, {}),
      });
      await expectMetaError(fetchQuota(file('1')), 'request_failed', status);
    }
  });

  test('parses bodyText when the parsed body is absent', async () => {
    const fetchQuota = createMetaQuotaFetcher({
      ...defaults,
      request: async () => ({
        statusCode: 200,
        header: {},
        body: null,
        bodyText: '{"subs_usage":{"weekly":{"used_percent":12}}}',
      }),
    });
    expect((await fetchQuota(file('1'))).windows[1].usedPercent).toBe(12);
  });
});
