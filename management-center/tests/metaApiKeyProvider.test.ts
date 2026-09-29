import { afterEach, describe, expect, test } from 'bun:test';
import { metaToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import { apiClient } from '../src/services/api/client';
import { providersApi } from '../src/services/api/providers';
import { normalizeConfigResponse } from '../src/services/api/transformers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
const originalDelete = apiClient.delete;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
  apiClient.delete = originalDelete;
});

describe('Meta Muse API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'meta-api-key': [
        {
          'api-key': 'meta-secret',
          priority: 7,
          weight: 3,
          prefix: 'muse',
          'base-url': 'https://api.meta.ai/v1',
          'proxy-url': 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
          'excluded-models': ['muse-spark-1.1'],
          'disable-cooling': true,
          'auth-index': 'meta:apikey:1',
        },
      ],
    });

    expect(config.metaApiKeys).toEqual([
      {
        apiKey: 'meta-secret',
        priority: 7,
        weight: 3,
        prefix: 'muse',
        baseUrl: 'https://api.meta.ai/v1',
        proxyUrl: 'socks5://proxy.example:1080',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
        excludedModels: ['muse-spark-1.1'],
        disableCooling: true,
        authIndex: 'meta:apikey:1',
      },
    ]);

    const resource = metaToResource(config.metaApiKeys![0], 0);
    expect(resource.brand).toBe('meta');
    expect(resource.baseUrl).toBe('https://api.meta.ai/v1');
    expect(resource.models).toEqual(['muse-spark-1.3']);
    expect(resource.selector).toEqual({
      brand: 'meta',
      apiKey: 'meta-secret',
      baseUrl: 'https://api.meta.ai/v1',
      index: 0,
    });
    expect(
      buildProviderGroups(config).find((group) => group.id === 'meta')?.resources
    ).toHaveLength(1);
    expect(PROVIDER_DESCRIPTORS.meta.baseUrlRequired).toBe(false);
    expect(PROVIDER_DESCRIPTORS.meta.supportsWebsockets).toBe(false);
    expect(PROVIDER_DESCRIPTORS.meta.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('meta')).toBe(PROVIDER_BRAND_ORDER.indexOf('codex') + 1);
    expect(MODEL_DISCOVERY_BRANDS).toContain('meta');
  });

  test('lists Meta keys through the dedicated management endpoint', async () => {
    apiClient.get = (async (url: string) => {
      expect(url).toBe('/meta-api-key');
      return {
        'meta-api-key': [
          {
            'api-key': 'meta-key',
            'base-url': 'https://api.meta.ai/v1',
            'auth-index': 'meta:apikey:0',
          },
        ],
      };
    }) as typeof apiClient.get;

    await expect(providersApi.getMetaConfigs()).resolves.toEqual([
      {
        apiKey: 'meta-key',
        baseUrl: 'https://api.meta.ai/v1',
        authIndex: 'meta:apikey:0',
      },
    ]);
  });

  test('creates, updates, and deletes keys while preserving unknown backend fields', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    let configResponse: unknown = {
      'meta-api-key': [
        {
          'api-key': 'existing',
          'base-url': 'https://api.meta.ai/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
      ],
    };
    apiClient.get = (async (url: string) => {
      calls.push({ method: 'GET', url });
      return configResponse;
    }) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      calls.push({ method: 'PUT', url, data });
      configResponse = { 'meta-api-key': data };
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createMetaConfig({
      apiKey: 'meta-new',
      weight: 4,
      prefix: 'muse',
      baseUrl: 'https://api.meta.ai/v1',
      proxyUrl: 'direct',
      models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
      excludedModels: ['muse-spark-1.1'],
      disableCooling: true,
    });
    await providersApi.updateMetaConfig('existing', 'https://api.meta.ai/v1', {
      apiKey: 'existing',
      priority: 9,
      baseUrl: 'https://api.meta.ai/v1',
      models: [{ name: 'muse-spark-1.3' }],
    });
    await providersApi.deleteMetaConfig('existing', 'https://api.meta.ai/v1');

    expect(calls[1]).toEqual({
      method: 'PUT',
      url: '/meta-api-key',
      data: [
        {
          'api-key': 'existing',
          'base-url': 'https://api.meta.ai/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
        {
          'api-key': 'meta-new',
          weight: 4,
          prefix: 'muse',
          'base-url': 'https://api.meta.ai/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
          'excluded-models': ['muse-spark-1.1'],
        },
      ],
    });
    expect(calls[3]).toEqual({
      method: 'PUT',
      url: '/meta-api-key',
      data: [
        {
          'request-retry': 2,
          'future-field': 'preserved',
          'api-key': 'existing',
          priority: 9,
          'base-url': 'https://api.meta.ai/v1',
          models: [{ name: 'muse-spark-1.3' }],
        },
        {
          'api-key': 'meta-new',
          weight: 4,
          prefix: 'muse',
          'base-url': 'https://api.meta.ai/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
          'excluded-models': ['muse-spark-1.1'],
        },
      ],
    });
    expect(calls[4]).toEqual({
      method: 'DELETE',
      url: '/meta-api-key?api-key=existing&base-url=https%3A%2F%2Fapi.meta.ai%2Fv1',
    });
  });
});
