import { afterEach, describe, expect, test } from 'bun:test';
import {
  buildInteractionsEndpoint,
  buildInteractionsProbePayload,
  getProviderUsageKey,
  INTERACTIONS_API_REVISION,
} from '../src/components/providers/utils';
import { interactionsToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
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

describe('Interactions API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'interactions-api-key': [
        {
          'api-key': 'interactions-secret',
          priority: 8,
          weight: 3,
          prefix: 'native',
          'base-url': 'https://generativelanguage.googleapis.com',
          'proxy-url': 'direct',
          headers: { 'X-Custom': 'value' },
          models: [
            {
              name: 'gemini-3.1-flash-lite',
              alias: 'native-flash',
              thinking: { levels: ['low', 'medium', 'high'] },
            },
          ],
          'excluded-models': ['gemini-2.5-*'],
          'disable-cooling': true,
          'auth-index': 'gemini-interactions:apikey:1',
        },
      ],
    });

    expect(config.interactionsApiKeys).toEqual([
      {
        apiKey: 'interactions-secret',
        priority: 8,
        weight: 3,
        prefix: 'native',
        baseUrl: 'https://generativelanguage.googleapis.com',
        proxyUrl: 'direct',
        headers: { 'X-Custom': 'value' },
        models: [
          {
            name: 'gemini-3.1-flash-lite',
            alias: 'native-flash',
            thinking: { levels: ['low', 'medium', 'high'] },
          },
        ],
        excludedModels: ['gemini-2.5-*'],
        disableCooling: true,
        authIndex: 'gemini-interactions:apikey:1',
      },
    ]);

    const resource = interactionsToResource(config.interactionsApiKeys![0], 0);
    expect(resource.brand).toBe('interactions');
    expect(resource.models).toEqual(['gemini-3.1-flash-lite']);
    expect(resource.selector).toEqual({
      brand: 'interactions',
      apiKey: 'interactions-secret',
      baseUrl: 'https://generativelanguage.googleapis.com',
      index: 0,
    });
    expect(PROVIDER_DESCRIPTORS.interactions.baseUrlRequired).toBe(false);
    expect(PROVIDER_DESCRIPTORS.interactions.supportsTestModel).toBe(true);
    expect(PROVIDER_BRAND_ORDER.indexOf('interactions')).toBe(
      PROVIDER_BRAND_ORDER.indexOf('gemini') + 1
    );
    expect(MODEL_DISCOVERY_BRANDS).toContain('interactions');
  });

  test('builds the native interactions endpoint from supported base URL forms', () => {
    expect(buildInteractionsEndpoint('')).toBe(
      'https://generativelanguage.googleapis.com/v1beta/interactions'
    );
    expect(buildInteractionsEndpoint('https://generativelanguage.googleapis.com')).toBe(
      'https://generativelanguage.googleapis.com/v1beta/interactions'
    );
    expect(buildInteractionsEndpoint('https://example.com/v1beta')).toBe(
      'https://example.com/v1beta/interactions'
    );
    expect(buildInteractionsEndpoint('https://example.com/v1beta/interactions')).toBe(
      'https://example.com/v1beta/interactions'
    );
  });

  test('uses the documented revision and minimal non-streaming probe body', () => {
    expect(INTERACTIONS_API_REVISION).toBe('2026-05-20');
    expect(buildInteractionsProbePayload('gemini-3.6-flash')).toEqual({
      model: 'gemini-3.6-flash',
      input: 'Hi',
    });
  });

  test('maps the UI brand to the backend runtime usage provider', () => {
    expect(getProviderUsageKey('interactions')).toBe('gemini-interactions');
    expect(getProviderUsageKey('gemini')).toBe('gemini');
    expect(getProviderUsageKey('claude')).toBe('claude');
  });

  test('updates only the matching key and base URL while preserving unknown fields', async () => {
    let putData: unknown;
    apiClient.get = (async () => ({
      'interactions-api-key': [
        {
          'api-key': 'shared-key',
          'base-url': 'https://first.example.com',
          'future-field': 'first',
        },
        {
          'api-key': 'shared-key',
          'base-url': 'https://second.example.com',
          'proxy-url': 'direct',
          headers: { 'X-Old': 'value' },
          'excluded-models': ['old-model'],
          'disable-cooling': true,
          'future-field': 'preserved',
          'auth-index': 'response-only',
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      putData = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateInteractionsKey('shared-key', 'https://second.example.com', {
      apiKey: 'shared-key',
      baseUrl: 'https://updated.example.com',
      models: [{ name: 'gemini-3.1-flash-lite', alias: 'native-flash' }],
    });

    expect(putData).toEqual([
      {
        'api-key': 'shared-key',
        'base-url': 'https://first.example.com',
        'future-field': 'first',
      },
      {
        'future-field': 'preserved',
        'api-key': 'shared-key',
        'base-url': 'https://updated.example.com',
        models: [{ name: 'gemini-3.1-flash-lite', alias: 'native-flash' }],
      },
    ]);
  });

  test('creates and deletes keys through the interactions management endpoints', async () => {
    const calls: Array<{ method: string; url: string; data?: unknown }> = [];
    apiClient.get = (async (url: string) => {
      calls.push({ method: 'GET', url });
      return {
        'interactions-api-key': [
          {
            'api-key': 'existing',
            'base-url': 'https://generativelanguage.googleapis.com',
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

    await providersApi.createInteractionsKey({
      apiKey: 'interactions-new',
      priority: 4,
      weight: 2,
      prefix: 'native',
      baseUrl: 'https://generativelanguage.googleapis.com',
      proxyUrl: 'direct',
      headers: { 'X-Custom': 'value' },
      models: [
        {
          name: 'gemini-3.1-flash-lite',
          alias: 'native-flash',
          thinking: { min: 128, max: 8192, dynamic_allowed: true },
        },
      ],
      excludedModels: ['gemini-2.5-*'],
      disableCooling: true,
    });
    await providersApi.deleteInteractionsKey(
      'interactions-new',
      'https://generativelanguage.googleapis.com'
    );

    expect(calls).toEqual([
      { method: 'GET', url: '/config' },
      {
        method: 'PUT',
        url: '/interactions-api-key',
        data: [
          {
            'api-key': 'existing',
            'base-url': 'https://generativelanguage.googleapis.com',
            'future-field': 'preserved',
          },
          {
            'api-key': 'interactions-new',
            priority: 4,
            weight: 2,
            prefix: 'native',
            'base-url': 'https://generativelanguage.googleapis.com',
            'proxy-url': 'direct',
            'disable-cooling': true,
            headers: { 'X-Custom': 'value' },
            models: [
              {
                name: 'gemini-3.1-flash-lite',
                alias: 'native-flash',
                thinking: { min: 128, max: 8192, dynamic_allowed: true },
              },
            ],
            'excluded-models': ['gemini-2.5-*'],
          },
        ],
      },
      {
        method: 'DELETE',
        url: '/interactions-api-key?api-key=interactions-new&base-url=https%3A%2F%2Fgenerativelanguage.googleapis.com',
      },
    ]);
  });
});
