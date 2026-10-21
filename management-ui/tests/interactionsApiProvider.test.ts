import { describe, expect, test } from 'bun:test';
import {
  buildInteractionsEndpoint,
  buildInteractionsProbePayload,
  getProviderUsageKey,
  INTERACTIONS_API_REVISION,
} from '../src/components/providers/utils';
import { interactionsToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { normalizeConfigResponse } from '../src/services/api/transformers';

describe('Interactions API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'api-keys': {
        interactions: [
          {
            name: 'interactions-1',
            'base-url': 'https://generativelanguage.googleapis.com',
            keys: [
              {
                'api-key': 'interactions-secret',
                priority: 8,
                weight: 3,
                prefix: 'native',
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
          },
        ],
      },
    });

    expect(config.interactionsApiKeys).toMatchObject([
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
});
