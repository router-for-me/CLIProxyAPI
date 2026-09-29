import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import { providersApi } from '../src/services/api/providers';
import {
  normalizeGeminiKeyConfig,
  normalizeOpenAIProvider,
  normalizeProviderKeyConfig,
} from '../src/services/api/transformers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

describe('provider credential weight normalization', () => {
  test('reads weight for direct API key credentials', () => {
    expect(normalizeGeminiKeyConfig({ 'api-key': 'gemini-key', weight: 5 })?.weight).toBe(5);
    expect(normalizeProviderKeyConfig({ 'api-key': 'provider-key', weight: 0 })?.weight).toBe(0);
  });

  test('reads per-key weight for OpenAI-compatible providers', () => {
    const provider = normalizeOpenAIProvider({
      name: 'example',
      'base-url': 'https://example.com/v1',
      'api-key-entries': [{ 'api-key': 'key-a', weight: 3 }, { 'api-key': 'key-b' }],
    });

    expect(provider?.apiKeyEntries[0]?.weight).toBe(3);
    expect(provider?.apiKeyEntries[1]?.weight).toBeUndefined();
  });

  test('removes a cleared Vertex weight while preserving unknown fields', async () => {
    let written: unknown;
    apiClient.get = (async () => ({
      'vertex-api-key': [
        {
          'api-key': 'vertex-key',
          'base-url': 'https://vertex.example',
          weight: 9,
          'future-field': 'keep',
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      written = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateVertexConfig('vertex-key', 'https://vertex.example', {
      apiKey: 'vertex-key',
      baseUrl: 'https://vertex.example',
      weight: undefined,
    });

    expect(written).toEqual([
      {
        'api-key': 'vertex-key',
        'base-url': 'https://vertex.example',
        'future-field': 'keep',
      },
    ]);
  });

  test('writes and clears nested OpenAI-compatible key weights', async () => {
    let written: unknown;
    apiClient.get = (async () => ({
      'openai-compatibility': [
        {
          name: 'example',
          'base-url': 'https://example.com/v1',
          'api-key-entries': [
            { 'api-key': 'key-a', weight: 8, custom: 'keep-a' },
            { 'api-key': 'key-b', custom: 'keep-b' },
          ],
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (_url: string, data?: unknown) => {
      written = data;
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateOpenAIProvider('example', 0, {
      name: 'example',
      baseUrl: 'https://example.com/v1',
      apiKeyEntries: [
        { apiKey: 'key-a', weight: undefined },
        { apiKey: 'key-b', weight: 4 },
      ],
    });

    expect(written).toEqual([
      {
        name: 'example',
        'base-url': 'https://example.com/v1',
        'api-key-entries': [
          { 'api-key': 'key-a', custom: 'keep-a' },
          { 'api-key': 'key-b', custom: 'keep-b', weight: 4 },
        ],
      },
    ]);
  });
});
