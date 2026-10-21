import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import { providersApi } from '../src/services/api/providers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

describe('provider model thinking config', () => {
  test('serializes thinking overrides for Vertex models', async () => {
    let putData: unknown;
    apiClient.get = (async () => ({ 'api-keys': { vertex: [] } })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      putData = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createVertexConfig({
      apiKey: 'vertex-key',
      models: [
        {
          name: 'gemini-3-pro',
          alias: 'vertex-pro',
          thinking: {
            min: 128,
            max: 32768,
            zero_allowed: true,
            dynamic_allowed: true,
          },
        },
      ],
    });

    expect(putData).toEqual([
      {
        name: 'vertex-1',
        keys: [
          {
            'api-key': 'vertex-key',
            models: [
              {
                name: 'gemini-3-pro',
                alias: 'vertex-pro',
                thinking: {
                  min: 128,
                  max: 32768,
                  'zero-allowed': true,
                  'dynamic-allowed': true,
                },
              },
            ],
          },
        ],
      },
    ]);
  });

  test('can clear thinking while preserving unknown model fields', async () => {
    let putData: unknown;
    apiClient.get = (async () => ({
      'api-keys': {
        codex: [
          {
            name: 'codex-1',
            'base-url': 'https://example.com',
            keys: [
              {
                'api-key': 'codex-key',
                models: [
                  {
                    name: 'gpt-5-codex',
                    alias: 'codex-latest',
                    thinking: { levels: ['low', 'high'] },
                    'future-field': 'preserved',
                  },
                ],
              },
            ],
          },
        ],
      },
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      putData = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateCodexConfig('codex-key', 'https://example.com', {
      apiKey: 'codex-key',
      baseUrl: 'https://example.com',
      models: [{ name: 'gpt-5-codex', alias: 'codex-latest' }],
    });

    expect(putData).toEqual([
      {
        name: 'codex-1',
        'base-url': 'https://example.com',
        keys: [
          {
            'api-key': 'codex-key',
            models: [
              {
                'future-field': 'preserved',
                name: 'gpt-5-codex',
                alias: 'codex-latest',
              },
            ],
          },
        ],
      },
    ]);
  });
});
