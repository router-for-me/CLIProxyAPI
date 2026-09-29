import { describe, expect, test } from 'bun:test';
import { openaiToResource } from '../src/features/providers/adapters';
import {
  buildQiniuCloudRaw,
  QINIU_CLOUD_BASE_URL_OPTIONS,
  QINIU_CLOUD_PROVIDER_NAME,
} from '../src/features/providers/qiniuCloud';
import {
  APIKEY_FUN_OPENAI_BASE_URL,
  APIKEY_FUN_PROVIDER_NAME,
  buildApiKeyFunRaw,
} from '../src/features/providers/sponsor';
import { normalizeConfigResponse } from '../src/services/api/transformers';

const openAIConfig = (name: string, baseUrl: string) => ({
  openaiCompatibility: [
    {
      name,
      baseUrl,
      apiKeyEntries: [{ apiKey: 'test-key' }],
    },
  ],
});

const customOpenAIConfig = (name: string) => openAIConfig(name, 'https://gateway.example.com/v1');

const mixedOpenAIConfig = (name: string, officialBaseUrl: string) => ({
  openaiCompatibility: [
    {
      name,
      baseUrl: officialBaseUrl,
      apiKeyEntries: [{ apiKey: 'official-key' }],
    },
    {
      name,
      baseUrl: 'https://gateway.example.com/v1',
      apiKeyEntries: [{ apiKey: 'custom-key' }],
    },
  ],
});

describe('sponsor custom endpoint isolation', () => {
  test('keeps APIKEY.FUN-named custom endpoints in the generic OpenAI group', () => {
    expect(buildApiKeyFunRaw(customOpenAIConfig(APIKEY_FUN_PROVIDER_NAME)).openai).toEqual([]);
  });

  test('keeps Qiniu-named custom endpoints in the generic OpenAI group', () => {
    expect(buildQiniuCloudRaw(customOpenAIConfig(QINIU_CLOUD_PROVIDER_NAME)).openai).toEqual([]);
  });

  test('keeps same-name custom entries outside sponsor delete targets', () => {
    expect(
      buildApiKeyFunRaw(
        mixedOpenAIConfig(APIKEY_FUN_PROVIDER_NAME, APIKEY_FUN_OPENAI_BASE_URL)
      ).openai.map((item) => item.index)
    ).toEqual([0]);
    expect(
      buildQiniuCloudRaw(
        mixedOpenAIConfig(QINIU_CLOUD_PROVIDER_NAME, QINIU_CLOUD_BASE_URL_OPTIONS[0].openaiBaseUrl)
      ).openai.map((item) => item.index)
    ).toEqual([0]);
  });

  test('keeps backend indexes when normalization filters an unnamed item', () => {
    const config = normalizeConfigResponse({
      'openai-compatibility': [
        { 'base-url': 'https://invalid.example.com/v1' },
        {
          name: APIKEY_FUN_PROVIDER_NAME,
          'base-url': APIKEY_FUN_OPENAI_BASE_URL,
          'api-key-entries': [{ 'api-key': 'official-a' }],
        },
        {
          name: APIKEY_FUN_PROVIDER_NAME,
          'base-url': 'https://gateway.example.com/v1',
          'api-key-entries': [{ 'api-key': 'custom-key' }],
        },
        {
          name: APIKEY_FUN_PROVIDER_NAME,
          'base-url': APIKEY_FUN_OPENAI_BASE_URL,
          'api-key-entries': [{ 'api-key': 'official-b' }],
        },
      ],
    });

    expect(config.openaiCompatibility?.map((item) => item.sourceIndex)).toEqual([1, 2, 3]);
    expect(buildApiKeyFunRaw(config).openai.map((item) => item.index)).toEqual([1, 3]);
    expect(openaiToResource(config.openaiCompatibility![1], 1).originalIndex).toBe(2);
  });

  test('still aggregates each sponsor official OpenAI endpoint', () => {
    expect(
      buildApiKeyFunRaw(openAIConfig('custom-name', APIKEY_FUN_OPENAI_BASE_URL)).openai.length
    ).toBe(1);
    expect(
      buildQiniuCloudRaw(openAIConfig('custom-name', QINIU_CLOUD_BASE_URL_OPTIONS[0].openaiBaseUrl))
        .openai.length
    ).toBe(1);
  });
});
