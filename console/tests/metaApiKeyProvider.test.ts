import { describe, expect, test } from 'bun:test';
import { metaToResource } from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import { normalizeConfigResponse } from '../src/services/api/transformers';

describe('Meta Muse API key provider', () => {
  test('normalizes the backend contract and exposes a dedicated workbench resource', () => {
    const config = normalizeConfigResponse({
      'api-keys': {
        meta: [
          {
            name: 'meta-1',
            'base-url': 'https://api.meta.ai/v1',
            keys: [
              {
                'api-key': 'meta-secret',
                priority: 7,
                weight: 3,
                prefix: 'muse',
                'proxy-url': 'socks5://proxy.example:1080',
                headers: { 'X-Custom': 'value' },
                models: [{ name: 'muse-spark-1.3', alias: 'muse-latest' }],
                'excluded-models': ['muse-spark-1.1'],
                'disable-cooling': true,
                'auth-index': 'meta:apikey:1',
              },
            ],
          },
        ],
      },
    });

    expect(config.metaApiKeys).toMatchObject([
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
});
