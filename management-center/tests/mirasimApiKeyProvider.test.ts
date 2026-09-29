import { afterEach, describe, expect, test } from 'bun:test';
import { mirasimToResource } from '../src/features/providers/adapters';
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

describe('Mirasim API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'mirasim-api-key': [
        {
          'api-key': 'mirasim-secret',
          priority: 7,
          weight: 3,
          prefix: 'ms',
          'base-url': 'https://mirasim.example.com/v1',
          'proxy-url': 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
          'excluded-models': ['claude-3-5-sonnet'],
          'disable-cooling': true,
          'auth-index': 'mirasim:apikey:1',
        },
      ],
    });

    expect(config.mirasimApiKeys).toEqual([
      {
        apiKey: 'mirasim-secret',
        priority: 7,
        weight: 3,
        prefix: 'ms',
        baseUrl: 'https://mirasim.example.com/v1',
        proxyUrl: 'socks5://proxy.example:1080',
        headers: { 'X-Custom': 'value' },
        models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
        excludedModels: ['claude-3-5-sonnet'],
        disableCooling: true,
        authIndex: 'mirasim:apikey:1',
      },
    ]);

    const resource = mirasimToResource(config.mirasimApiKeys![0], 0);
    expect(resource.brand).toBe('mirasim');
    expect(resource.baseUrl).toBe('https://mirasim.example.com/v1');
    expect(resource.models).toEqual(['claude-sonnet-4-5']);
    expect(resource.selector).toEqual({
      brand: 'mirasim',
      apiKey: 'mirasim-secret',
      baseUrl: 'https://mirasim.example.com/v1',
      index: 0,
    });
    expect(
      buildProviderGroups(config).find((group) => group.id === 'mirasim')?.resources
    ).toHaveLength(1);
    // Base URL is mandatory for Mirasim upstreams; cloak/fingerprint are Claude-only.
    expect(PROVIDER_DESCRIPTORS.mirasim.baseUrlRequired).toBe(true);
    expect(PROVIDER_DESCRIPTORS.mirasim.supportsCloak).toBe(false);
    expect(PROVIDER_DESCRIPTORS.mirasim.supportsWebsockets).toBe(false);
    expect(PROVIDER_DESCRIPTORS.mirasim.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('mirasim')).toBe(
      PROVIDER_BRAND_ORDER.indexOf('claude') + 1
    );
    expect(MODEL_DISCOVERY_BRANDS).toContain('mirasim');
  });

  test('lists Mirasim keys through the dedicated management endpoint', async () => {
    apiClient.get = (async (url: string) => {
      expect(url).toBe('/mirasim-api-key');
      return {
        'mirasim-api-key': [
          {
            'api-key': 'mirasim-key',
            'base-url': 'https://mirasim.example.com/v1',
            'auth-index': 'mirasim:apikey:0',
          },
        ],
      };
    }) as typeof apiClient.get;

    await expect(providersApi.getMirasimConfigs()).resolves.toEqual([
      {
        apiKey: 'mirasim-key',
        baseUrl: 'https://mirasim.example.com/v1',
        authIndex: 'mirasim:apikey:0',
      },
    ]);
  });

  test('creates, updates, and deletes keys while preserving unknown backend fields', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    let configResponse: unknown = {
      'mirasim-api-key': [
        {
          'api-key': 'existing',
          'base-url': 'https://mirasim.example.com/v1',
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
      configResponse = { 'mirasim-api-key': data };
      return undefined;
    }) as typeof apiClient.put;
    apiClient.delete = (async (url: string) => {
      calls.push({ method: 'DELETE', url });
      return undefined;
    }) as typeof apiClient.delete;

    await providersApi.createMirasimConfig({
      apiKey: 'mirasim-new',
      weight: 4,
      prefix: 'ms',
      baseUrl: 'https://mirasim.example.com/v1',
      proxyUrl: 'direct',
      models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
      excludedModels: ['claude-3-5-sonnet'],
      disableCooling: true,
    });
    await providersApi.updateMirasimConfig('existing', 'https://mirasim.example.com/v1', {
      apiKey: 'existing',
      priority: 9,
      baseUrl: 'https://mirasim.example.com/v1',
      models: [{ name: 'claude-sonnet-4-5' }],
    });
    await providersApi.deleteMirasimConfig('existing', 'https://mirasim.example.com/v1');

    expect(calls[1]).toEqual({
      method: 'PUT',
      url: '/mirasim-api-key',
      data: [
        {
          'api-key': 'existing',
          'base-url': 'https://mirasim.example.com/v1',
          'request-retry': 2,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
        {
          'api-key': 'mirasim-new',
          weight: 4,
          prefix: 'ms',
          'base-url': 'https://mirasim.example.com/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
          'excluded-models': ['claude-3-5-sonnet'],
        },
      ],
    });
    expect(calls[3]).toEqual({
      method: 'PUT',
      url: '/mirasim-api-key',
      data: [
        {
          'request-retry': 2,
          'future-field': 'preserved',
          'api-key': 'existing',
          priority: 9,
          'base-url': 'https://mirasim.example.com/v1',
          models: [{ name: 'claude-sonnet-4-5' }],
        },
        {
          'api-key': 'mirasim-new',
          weight: 4,
          prefix: 'ms',
          'base-url': 'https://mirasim.example.com/v1',
          'proxy-url': 'direct',
          'disable-cooling': true,
          models: [{ name: 'claude-sonnet-4-5', alias: 'sonnet' }],
          'excluded-models': ['claude-3-5-sonnet'],
        },
      ],
    });
    expect(calls[4]).toEqual({
      method: 'DELETE',
      url: '/mirasim-api-key?api-key=existing&base-url=https%3A%2F%2Fmirasim.example.com%2Fv1',
    });
  });

  test('never serializes Claude-only cloak or fingerprint fields', async () => {
    const payloads: unknown[] = [];
    apiClient.get = (async () => ({ 'mirasim-api-key': [] })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      expect(url).toBe('/mirasim-api-key');
      payloads.push(data);
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createMirasimConfig({
      apiKey: 'mirasim-new',
      baseUrl: 'https://mirasim.example.com/v1',
    });

    expect(JSON.stringify(payloads[0])).not.toContain('cloak');
    expect(JSON.stringify(payloads[0])).not.toContain('fingerprint');
  });
});
