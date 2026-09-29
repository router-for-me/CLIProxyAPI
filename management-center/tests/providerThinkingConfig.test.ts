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
    apiClient.get = (async () => ({ 'vertex-api-key': [] })) as typeof apiClient.get;
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
        'api-key': 'vertex-key',
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
      },
    ]);
  });

  test('can clear thinking while preserving unknown model fields', async () => {
    let putData: unknown;
    apiClient.get = (async () => ({
      'codex-api-key': [
        {
          'api-key': 'codex-key',
          'base-url': 'https://example.com',
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
        'api-key': 'codex-key',
        'base-url': 'https://example.com',
        models: [
          {
            'future-field': 'preserved',
            name: 'gpt-5-codex',
            alias: 'codex-latest',
          },
        ],
      },
    ]);
  });
});
