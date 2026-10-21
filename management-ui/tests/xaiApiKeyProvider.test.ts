import { describe, expect, test } from 'bun:test';
import { xaiToResource } from '../src/features/providers/adapters';
import { PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { normalizeConfigResponse } from '../src/services/api/transformers';

describe('xAI API key provider', () => {
  test('normalizes the backend xai-api-key contract and exposes a workbench resource', () => {
    const config = normalizeConfigResponse({
      'api-keys': {
        xai: [
          {
            name: 'xai-1',
            'base-url': 'https://api.x.ai/v1',
            keys: [
              {
                'api-key': 'xai-secret',
                priority: 7,
                prefix: 'team-xai',
                websockets: true,
                'proxy-url': 'http://proxy.local',
                headers: { 'X-Custom': 'value' },
                models: [{ name: 'grok-4.5', alias: 'grok-latest' }],
                'excluded-models': ['grok-3-*'],
                'disable-cooling': true,
                'auth-index': 'xai:apikey:1',
              },
            ],
          },
        ],
      },
    });

    expect(config.xaiApiKeys).toMatchObject([
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
});
