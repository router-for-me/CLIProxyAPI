import { afterEach, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { providersApi, type ProviderFamily } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import { serializeModelOptions } from '@/services/api/providerModels';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});
function backend(family: ProviderFamily, group: Record<string, unknown>) {
  let stored = structuredClone(group);
  apiClient.get = (async () => ({ 'api-keys': { [family]: [stored] } })) as typeof apiClient.get;
  apiClient.put = (async (_path: string, value: Record<string, unknown>[]) => {
    stored = structuredClone(value[0]);
  }) as typeof apiClient.put;
  return () => stored;
}
const rawModel = {
  name: 'fixture-model',
  alias: 'fixture-alias',
  'display-name': 'Fixture Model',
  'max-context-length': 32768,
  'force-mapping': true,
  'is-compat': true,
  'support-configuration-update': false,
  thinking: {
    min: 0,
    max: 8192,
    'zero-allowed': false,
    'dynamic-allowed': true,
    levels: ['low', 'high'],
  },
};
test('normalizes model attributes and converts v8 YAML thinking flags at the API boundary', () => {
  const config = normalizeProviderGroups([
    { name: 'fixture', keys: [{ 'api-key': 'fixture-key', models: [rawModel] }] },
  ])[0] as ProviderKeyConfig;
  expect(config.models?.[0]).toMatchObject({
    displayName: 'Fixture Model',
    maxContextLength: 32768,
    forceMapping: true,
    isCompat: true,
    supportConfigurationUpdate: false,
    thinking: {
      min: 0,
      max: 8192,
      zero_allowed: false,
      dynamic_allowed: true,
      levels: ['low', 'high'],
    },
  });
  expect(config.models?.[0].thinking).not.toHaveProperty('zero-allowed');
  expect(serializeModelOptions(config.models![0])).toMatchObject({
    ...Object.fromEntries(
      Object.entries(rawModel).filter(([key]) => key !== 'name' && key !== 'alias')
    ),
  });
});
test.each(['gemini', 'interactions', 'claude', 'codex', 'meta', 'xai'] as const)(
  '%s preserves options on unrelated edits and persists explicit false/zero',
  async (family) => {
    const stored = backend(family, {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [rawModel] }],
    });
    const config = normalizeProviderGroups([stored()])[0] as ProviderKeyConfig;
    const update = {
      gemini: providersApi.updateGeminiKey,
      interactions: providersApi.updateInteractionsKey,
      claude: providersApi.updateClaudeConfig,
      codex: providersApi.updateCodexConfig,
      meta: providersApi.updateMetaConfig,
      xai: providersApi.updateXAIConfig,
    }[family];
    await update(config.apiKey, config.baseUrl, { ...config, priority: 4 });
    expect((stored().keys as ProviderKeyConfig[])[0].models).toEqual([rawModel]);
    const current = normalizeProviderGroups([stored()])[0] as ProviderKeyConfig;
    await update(current.apiKey, undefined, {
      ...current,
      models: [
        {
          ...current.models![0],
          displayName: undefined,
          maxContextLength: 0,
          forceMapping: false,
          isCompat: false,
        },
      ],
    });
    const model = (
      (stored().keys as Record<string, unknown>[])[0].models as Record<string, unknown>[]
    )[0];
    expect(model).not.toHaveProperty('display-name');
    expect(model).toMatchObject({
      'max-context-length': 0,
      'force-mapping': false,
      'is-compat': false,
      thinking: rawModel.thinking,
    });
  }
);
test('Vertex serializes only supported model attributes with canonical thinking flags', async () => {
  const stored = backend('vertex', {
    name: 'fixture',
    keys: [{ 'api-key': 'fixture-key', models: [{ name: 'm', alias: 'alias' }] }],
  });
  const config = normalizeProviderGroups([stored()])[0] as ProviderKeyConfig;
  await providersApi.updateVertexConfig(config.apiKey, undefined, {
    ...config,
    models: [
      {
        ...config.models![0],
        displayName: 'Vertex',
        forceMapping: true,
        maxContextLength: 100,
        isCompat: true,
        thinking: { zero_allowed: true, dynamic_allowed: false },
      },
    ],
  });
  expect(((stored().keys as Record<string, unknown>[])[0].models as unknown[])[0]).toEqual({
    name: 'm',
    alias: 'alias',
    'display-name': 'Vertex',
    'force-mapping': true,
    thinking: { 'zero-allowed': true, 'dynamic-allowed': false },
  });
});
test('OpenAI multimodal and token options round trip and can be cleared', async () => {
  const stored = backend('openai-compatibility', {
    name: 'fixture',
    'base-url': 'https://example.invalid',
    keys: [{ 'api-key': 'fixture-key' }],
    models: [
      {
        name: 'm',
        'input-modalities': ['text', 'image'],
        'output-modalities': ['text'],
        'use-max-completion-tokens': true,
        thinking: {},
      },
    ],
  });
  const config = normalizeProviderGroups([stored()], true)[0] as OpenAIProviderConfig;
  expect(config.models![0]).toMatchObject({
    inputModalities: ['text', 'image'],
    outputModalities: ['text'],
    useMaxCompletionTokens: true,
    thinking: {},
  });
  await providersApi.updateOpenAIProvider(config.name, 0, {
    ...config,
    models: [
      {
        ...config.models![0],
        inputModalities: undefined,
        outputModalities: [],
        useMaxCompletionTokens: false,
        thinking: undefined,
      },
    ],
  });
  expect((stored().models as unknown[])[0]).toEqual({
    name: 'm',
    'output-modalities': [],
    'use-max-completion-tokens': false,
  });
});
