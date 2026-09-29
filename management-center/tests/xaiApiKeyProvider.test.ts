import { afterEach, describe, expect, test } from 'bun:test';
import { xaiToResource } from '../src/features/providers/adapters';
import { PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
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

describe('xAI API key provider', () => {
  test('normalizes the backend xai-api-key contract and exposes a workbench resource', () => {
    const config = normalizeConfigResponse({
      'xai-api-key': [
        {
          'api-key': 'xai-secret',
          priority: 7,
          prefix: 'team-xai',
          'base-url': 'https://api.x.ai/v1',
          websockets: true,
          'proxy-url': 'http://proxy.local',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
          'excluded-models': ['grok-3-*'],
          'disable-cooling': true,
          'auth-index': 'xai:apikey:1',
        },
      ],
    });

    expect(config.xaiApiKeys).toEqual([
      {
        apiKey: 'xai-secret',
        priority: 7,
        prefix: 'team-xai',
        baseUrl: 'https://api.x.ai/v1',
        websockets: true,
        proxyUrl: 'http://proxy.local',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
        excludedModels: ['grok-3-*'],
        disableCooling: true,
        authIndex: 'xai:apikey:1',
      },
    ]);

    const resource = xaiToResource(config.xaiApiKeys![0], 0);
    expect(resource.brand).toBe('xai');
    expect(resource.baseUrl).toBe('https://api.x.ai/v1');
    expect(resource.models).toEqual(['grok-4.5']);
    expect(resource.flags.websockets).toBe(true);
    expect(resource.selector).toEqual({
      brand: 'xai',
      apiKey: 'xai-secret',
      baseUrl: 'https://api.x.ai/v1',
      index: 0,
    });
    expect(PROVIDER_DESCRIPTORS.xai.baseUrlRequired).toBe(true);
    expect(PROVIDER_DESCRIPTORS.xai.supportsWebsockets).toBe(true);
  });

  test('creates and deletes xAI keys through the backend management contract', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    apiClient.get = (async (url: string) => {
      calls.push({ method: 'GET', url });
      return {
        'xai-api-key': [
          {
            'api-key': 'existing',
            'base-url': 'https://api.x.ai/v1',
            'future-field': 'preserved',
          },
        ],
      };
    }) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      calls.push({ method: 'PUT', url, data });
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createXAIConfig({
      apiKey: 'xai-new',
      priority: 3,
      prefix: 'xai',
      baseUrl: 'https://api.x.ai/v1',
      websockets: true,
      proxyUrl: 'direct',
      headers: { 'X-Custom': 'value' },
      models: [
        {
          name: 'grok-4.5',
          alias: 'grok-latest',
          thinking: { levels: ['low', 'high', 'xhigh'] },
        },
      ],
      excludedModels: ['grok-3-*'],
      disableCooling: true,
    });
    await providersApi.deleteXAIConfig('xai-new', 'https://api.x.ai/v1');

    expect(calls).toEqual([
      { method: 'GET', url: '/config' },
      {
        method: 'PUT',
        url: '/xai-api-key',
        data: [
          {
            'api-key': 'existing',
            'base-url': 'https://api.x.ai/v1',
            'future-field': 'preserved',
          },
          {
            'api-key': 'xai-new',
            priority: 3,
            prefix: 'xai',
            'base-url': 'https://api.x.ai/v1',
            websockets: true,
            'proxy-url': 'direct',
            headers: { 'X-Custom': 'value' },
            models: [
              {
                name: 'grok-4.5',
                alias: 'grok-latest',
                thinking: { levels: ['low', 'high', 'xhigh'] },
              },
            ],
            'excluded-models': ['grok-3-*'],
            'disable-cooling': true,
          },
        ],
      },
      {
        method: 'DELETE',
        url: '/xai-api-key?api-key=xai-new&base-url=https%3A%2F%2Fapi.x.ai%2Fv1',
      },
    ]);
  });
});
