import { describe, expect, test } from 'bun:test';
import {
  buildInteractionsEndpoint,
  buildInteractionsProbePayload,
  getProviderUsageKey,
  INTERACTIONS_API_REVISION,
} from '../src/components/providers/utils';
import {
  interactionsToResource,
  metaToResource,
  xaiToResource,
} from '../src/features/providers/adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from '../src/features/providers/descriptors';
import { MODEL_DISCOVERY_BRANDS } from '../src/features/providers/sheets/forms/useModelDiscovery';
import { buildProviderGroups } from '../src/features/providers/useProviderWorkbench';
import { normalizeConfigResponse } from '../src/services/api/transformers';

const API_KEY_FAMILIES = [
  {
    brand: 'interactions',
    configKey: 'interactionsApiKeys',
    toResource: interactionsToResource,
    baseUrl: 'https://generativelanguage.googleapis.com',
    model: {
      name: 'gemini-3.1-flash-lite',
      alias: 'native-flash',
      thinking: { levels: ['low', 'medium', 'high'] },
    },
    websockets: false,
    baseUrlRequired: false,
    after: 'gemini',
  },
  {
    brand: 'meta',
    configKey: 'metaApiKeys',
    toResource: metaToResource,
    baseUrl: 'https://api.meta.ai/v1',
    model: { name: 'muse-spark-1.3', alias: 'muse-latest' },
    websockets: false,
    baseUrlRequired: false,
    after: 'codex',
  },
  {
    brand: 'xai',
    configKey: 'xaiApiKeys',
    toResource: xaiToResource,
    baseUrl: 'https://api.x.ai/v1',
    model: { name: 'grok-4.5', alias: 'grok-latest' },
    websockets: true,
    baseUrlRequired: true,
    after: 'meta',
  },
] as const;

describe('api-keys provider families', () => {
  test.each(API_KEY_FAMILIES)(
    '$brand normalizes the backend contract and exposes a workbench resource',
    ({ brand, configKey, toResource, baseUrl, model, websockets, baseUrlRequired, after }) => {
      const config = normalizeConfigResponse({
        'api-keys': {
          [brand]: [
            {
              name: `${brand}-1`,
              'base-url': baseUrl,
              keys: [
                {
                  'api-key': `${brand}-secret`,
                  priority: 7,
                  weight: 3,
                  prefix: 'team',
                  ...(websockets ? { websockets: true } : {}),
                  'proxy-url': 'socks5://proxy.example:1080',
                  headers: { 'X-Custom': 'value' },
                  models: [model],
                  'excluded-models': ['legacy-*'],
                  'disable-cooling': true,
                  'auth-index': `${brand}:apikey:1`,
                },
              ],
            },
          ],
        },
      });

      const entries = config[configKey]!;
      expect(entries).toMatchObject([
        {
          apiKey: `${brand}-secret`,
          priority: 7,
          weight: 3,
          prefix: 'team',
          baseUrl,
          proxyUrl: 'socks5://proxy.example:1080',
          headers: { 'X-Custom': 'value' },
          models: [model],
          excludedModels: ['legacy-*'],
          disableCooling: true,
          authIndex: `${brand}:apikey:1`,
          ...(websockets ? { websockets: true } : {}),
        },
      ]);

      const resource = toResource(entries[0], 0);
      expect(resource.brand).toBe(brand);
      expect(resource.baseUrl).toBe(baseUrl);
      expect(resource.models).toEqual([model.name]);
      expect(Boolean(resource.flags.websockets)).toBe(websockets);
      expect(resource.selector).toEqual({ brand, apiKey: `${brand}-secret`, baseUrl, index: 0 });
      expect(
        buildProviderGroups(config).find((group) => group.id === brand)?.resources
      ).toHaveLength(1);

      const descriptor = PROVIDER_DESCRIPTORS[brand];
      expect(descriptor.baseUrlRequired).toBe(baseUrlRequired);
      expect(descriptor.supportsWebsockets).toBe(websockets);
      expect(descriptor.supportsTestModel).toBe(true);
      expect(PROVIDER_BRAND_ORDER.indexOf(brand)).toBe(PROVIDER_BRAND_ORDER.indexOf(after) + 1);
      expect(MODEL_DISCOVERY_BRANDS).toContain(brand);
    }
  );

  test('every api-keys family the backend accepts gets a workbench tab', () => {
    const visibleBrands = buildProviderGroups(normalizeConfigResponse({})).map((group) => group.id);
    for (const family of ['gemini', 'interactions', 'codex', 'meta', 'xai', 'claude', 'vertex']) {
      expect(visibleBrands).toContain(family);
    }
  });
});

describe('Interactions API key provider', () => {
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
