import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { apiClient } from '../src/services/api/client';
import { apiCallApi, type ApiCallRequest } from '../src/services/api/apiCall';
import { normalizeProviderGroups } from '../src/services/api/transformers';
import type { ProviderKeyConfig } from '../src/types/provider';
import {
  useConnectivityTest,
  type UseConnectivityTestArgs,
  type ConnectivityErrorMessages,
} from '../src/features/providers/sheets/forms/useConnectivityTest';
import { useModelDiscovery } from '../src/features/providers/sheets/forms/useModelDiscovery';

function captureHook<T>(hook: () => T): T {
  let result: T;
  function Harness() {
    result = hook();
    return null;
  }
  renderToStaticMarkup(createElement(Harness));
  return result!;
}

const messages: ConnectivityErrorMessages = {
  baseUrlRequired: 'base required',
  endpointInvalid: 'invalid endpoint',
  apiKeyRequired: 'key required',
  modelRequired: 'model required',
  timeout: () => 'timeout',
  requestFailed: 'failed',
};
const success = { statusCode: 200, header: {}, bodyText: '', body: { data: [] } };
let requestSpy: ReturnType<typeof spyOn<typeof apiCallApi, 'request'>> | undefined;
afterEach(() => requestSpy?.mockRestore());

const args: UseConnectivityTestArgs = {
  brand: 'codex',
  baseUrl: 'https://upstream.example/v1',
  apiKey: 'fixture-key',
  proxyUrl: ' socks5://proxy.example:1080 ',
  models: [{ name: 'fixture-model', alias: '' }],
  formHeaders: [],
};

describe('provider probe proxy forwarding', () => {
  test.each(['codex', 'meta', 'xai', 'gemini', 'interactions', 'claude'] as const)(
    '%s connectivity uses the current form proxy',
    async (brand) => {
      requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
      const hook = captureHook(() => useConnectivityTest({ ...args, brand }, messages));
      if (brand === 'claude') await hook.runClaude();
      else if (brand === 'gemini' || brand === 'interactions') await hook.runGemini();
      else await hook.runCodex();
      expect(requestSpy.mock.calls[0][0].proxy_url).toBe('socks5://proxy.example:1080');
    }
  );

  const liveProxy = 'http://live.invalid';
  test.each([
    { name: 'unchanged blank', savedProxyUrl: '', key: '', indexed: true },
    { name: 'cleared with literal key', savedProxyUrl: 'direct', key: 'literal', indexed: false },
    {
      name: 'cleared with indexed token',
      savedProxyUrl: 'direct',
      key: '',
      indexed: true,
      expectedProxy: liveProxy,
    },
    {
      name: 'cleared without global proxy',
      savedProxyUrl: 'http://old.invalid',
      key: '',
      indexed: true,
      expectedProxy: 'direct',
    },
    {
      name: 'cleared with custom token header',
      savedProxyUrl: 'direct',
      key: 'literal',
      indexed: true,
      customToken: true,
      expectedProxy: liveProxy,
    },
  ])(
    '$name resolves transport at probe time',
    async ({ savedProxyUrl, key, indexed, customToken, expectedProxy }) => {
      const global = expectedProxy === 'direct' ? '' : liveProxy;
      const get = spyOn(apiClient, 'get').mockResolvedValue(global);
      try {
        requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
        const probe = {
          ...args,
          savedProxyUrl,
          proxyUrl: ' ',
          apiKey: key,
          authIndex: 'fixture-index',
          formHeaders: customToken ? [{ key: 'X-Probe', value: 'custom $TOKEN$' }] : [],
        };
        await captureHook(() => useConnectivityTest(probe, messages)).runCodex();
        await captureHook(() => useModelDiscovery(probe)).fetch();
        const entry = { apiKey: key, proxyUrl: ' ', savedProxyUrl, authIndex: probe.authIndex };
        const openai = { ...probe, brand: 'openaiCompatibility' as const, apiKeyEntries: [entry] };
        await captureHook(() => useConnectivityTest(openai, messages)).runOpenAIKey(0);
        await captureHook(() => useModelDiscovery(openai)).fetch();
        expect(get.mock.calls.map(([path]) => path)).toEqual(
          expectedProxy ? Array(4).fill('/config/requests/proxy-url') : []
        );
        expect(requestSpy.mock.calls).toHaveLength(4);
        for (const [request] of requestSpy.mock.calls) {
          expect(request.proxy_url).toBe(expectedProxy);
          expect(request.authIndex).toBe(indexed ? 'fixture-index' : undefined);
          expect(request.header?.Authorization).toBe(`Bearer ${key || '$TOKEN$'}`);
        }
      } finally {
        get.mockRestore();
      }
    }
  );

  test('failed global proxy read never sends an indexed clear or retries it anonymously', async () => {
    const get = spyOn(apiClient, 'get').mockRejectedValue(new Error('offline'));
    try {
      requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
      const probe = {
        ...args,
        brand: 'openaiCompatibility' as const,
        apiKeyEntries: [
          { apiKey: '', authIndex: 'fixture-index', proxyUrl: '', savedProxyUrl: 'direct' },
        ],
      };
      expect(await captureHook(() => useConnectivityTest(probe, messages)).runOpenAIKey(0)).toBe(
        false
      );
      await captureHook(() => useModelDiscovery(probe)).fetch();
      expect(requestSpy).not.toHaveBeenCalled();
    } finally {
      get.mockRestore();
    }
  });

  test('OpenAI batch tests preserve each key proxy, including direct and blank', async () => {
    requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
    const hook = captureHook(() =>
      useConnectivityTest(
        {
          ...args,
          brand: 'openaiCompatibility',
          apiKeyEntries: [' http://one.example:8080 ', 'direct', ''].map((proxyUrl) => ({
            apiKey: 'fixture-key',
            proxyUrl,
          })),
        },
        messages
      )
    );
    await hook.runOpenAIAllKeys();
    expect(requestSpy.mock.calls.map(([request]) => request.proxy_url)).toEqual([
      'http://one.example:8080',
      'direct',
      undefined,
    ]);
  });

  test.each(['codex', 'meta', 'xai', 'gemini', 'interactions', 'claude'] as const)(
    '%s discovery forwards the effective inherited proxy',
    async (brand) => {
      requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
      const [config] = normalizeProviderGroups([
        { 'base-url': args.baseUrl, 'proxy-url': 'direct', keys: [{ 'api-key': 'fixture-key' }] },
      ]) as ProviderKeyConfig[];
      const hook = captureHook(() =>
        useModelDiscovery({ ...args, brand, proxyUrl: config.proxyUrl })
      );
      await hook.fetch();
      expect(requestSpy.mock.calls[0][0].proxy_url).toBe('direct');
    }
  );

  test('OpenAI unauthenticated discovery retry keeps the selected key proxy', async () => {
    const requests: ApiCallRequest[] = [];
    requestSpy = spyOn(apiCallApi, 'request').mockImplementation(async (request) => {
      requests.push(request);
      return requests.length === 1 ? { ...success, statusCode: 401 } : success;
    });
    const hook = captureHook(() =>
      useModelDiscovery({
        ...args,
        brand: 'openaiCompatibility',
        apiKeyEntries: [{ apiKey: 'fixture-key', proxyUrl: ' direct ' }],
      })
    );
    await hook.fetch();
    expect(requests).toHaveLength(2);
    expect(requests.map((request) => request.proxy_url)).toEqual(['direct', 'direct']);
    expect(requests[0].header?.Authorization).toBe('Bearer fixture-key');
    expect(requests[1].header).toBeUndefined();
    expect(requests[1].authIndex).toBeUndefined();
  });
});

describe('OpenAI connectivity token limits (#572)', () => {
  const models = [
    { name: 'reasoner', alias: 'reasoning-alias', useMaxCompletionTokens: true },
    { name: 'legacy', alias: 'legacy-alias', useMaxCompletionTokens: false },
    { name: 'default', alias: '' },
    { name: 'alias-owner', alias: 'collision', useMaxCompletionTokens: true },
    { name: 'collision', alias: '', useMaxCompletionTokens: false },
    { name: ' Configured (high) ', alias: ' Friendly (low) ', useMaxCompletionTokens: true },
  ];

  test.each([
    ['reasoner', true],
    ['legacy', false],
    ['default', false],
    ['unknown', false],
    ['reasoning-alias', true],
    ['legacy-alias', false],
    ['collision', false],
    [' REASONER (high) ', true],
    [' REASONING-ALIAS(anything) ', true],
    ['configured', true],
    ['friendly', true],
    ['reasoner(unclosed', false],
    ['(high)', false],
    ['', true],
    ['   ', true],
    [undefined, true],
  ] as const)('selected model %s uses completion tokens: %s', async (testModel, completion) => {
    requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
    const hook = captureHook(() =>
      useConnectivityTest(
        {
          ...args,
          brand: 'openaiCompatibility',
          testModel,
          models: [{ name: ' ', alias: 'ignored' }, ...models],
          apiKeyEntries: [{ apiKey: '', authIndex: 'fixture-auth', proxyUrl: ' direct ' }],
          formHeaders: [{ key: 'X-Probe', value: 'fixture' }],
        },
        messages
      )
    );
    expect(await hook.runOpenAIKey(0)).toBe(true);
    const [request, options] = requestSpy.mock.calls[0];
    expect(JSON.parse(request.data!)).toEqual({
      model: testModel?.trim() || 'reasoner',
      messages: [{ role: 'user', content: 'Hi' }],
      stream: false,
      ...(completion ? { max_completion_tokens: 5 } : { max_tokens: 5 }),
    });
    expect(request.authIndex).toBe('fixture-auth');
    expect(request.proxy_url).toBe('direct');
    expect(request.header).toEqual({
      'Content-Type': 'application/json',
      Authorization: 'Bearer $TOKEN$',
      'X-Probe': 'fixture',
    });
    expect(options).toEqual({ timeout: 30_000 });
  });

  test('batch tests retain explicit authorization and use the selected model flag', async () => {
    requestSpy = spyOn(apiCallApi, 'request').mockResolvedValue(success);
    const hook = captureHook(() =>
      useConnectivityTest(
        {
          ...args,
          brand: 'openaiCompatibility',
          models,
          apiKeyEntries: [
            { apiKey: 'fixture-key', proxyUrl: 'direct' },
            { apiKey: '', existingApiKey: 'fixture-existing', proxyUrl: '' },
          ],
          formHeaders: [{ key: 'authorization', value: 'Bearer fixture-custom' }],
        },
        messages
      )
    );
    await hook.runOpenAIAllKeys();
    expect(requestSpy.mock.calls).toHaveLength(2);
    for (const [request] of requestSpy.mock.calls) {
      expect(request.header?.authorization).toBe('Bearer fixture-custom');
      expect(request.header?.Authorization).toBeUndefined();
      const body = JSON.parse(request.data!);
      expect(body.max_completion_tokens).toBe(5);
      expect(body).not.toHaveProperty('max_tokens');
    }
  });
});
