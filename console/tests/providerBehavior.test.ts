import { afterEach, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { apiClient } from '@/services/api/client';
import { providersApi, type ProviderFamily } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import { pickProviderBehavior } from '@/features/providers/providerBehavior';
import { ProviderBehaviorEditor } from '@/features/providers/sheets/forms/ProviderBehaviorEditor';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { ProviderBrand } from '@/features/providers/types';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});
function backend(family: ProviderFamily, initial: Record<string, unknown>[] = []) {
  let groups = structuredClone(initial);
  apiClient.get = (async () => ({ 'api-keys': { [family]: groups } })) as typeof apiClient.get;
  apiClient.put = (async (url: string, data: Record<string, unknown>[]) => {
    expect(url).toBe(`/config/api-keys/${family}`);
    groups = structuredClone(data);
  }) as typeof apiClient.put;
  return () => groups;
}
const key = (groups: Record<string, unknown>[]) => (groups[0].keys as Record<string, unknown>[])[0];
test('Codex creates, disables and clears credential behavior without modifying siblings', async () => {
  const groups = backend('codex');
  await providersApi.createCodexConfig({
    apiKey: 'fixture-key',
    baseUrl: 'https://example.invalid',
    alphaSearch: true,
    disableCodexCloaking: true,
  });
  expect(key(groups())).toEqual({
    'api-key': 'fixture-key',
    'alpha-search': true,
    'disable-codex-cloaking': true,
  });
  let config = normalizeProviderGroups(groups())[0] as ProviderKeyConfig;
  expect(pickProviderBehavior(config, 'codex')).toEqual({
    alphaSearch: true,
    disableCodexCloaking: true,
  });
  await providersApi.updateCodexConfig(config.apiKey, config.baseUrl, {
    ...config,
    alphaSearch: false,
    disableCodexCloaking: false,
  });
  expect(key(groups())).toMatchObject({ 'alpha-search': false, 'disable-codex-cloaking': false });
  config = normalizeProviderGroups(groups())[0] as ProviderKeyConfig;
  await providersApi.updateCodexConfig(config.apiKey, config.baseUrl, {
    ...config,
    disableCodexCloaking: undefined,
  });
  expect(key(groups())).not.toHaveProperty('disable-codex-cloaking');
  expect(key(groups())['alpha-search']).toBe(false);
});
test.each([undefined, null])('untouched Codex defaults remain absent/null (%s)', async (value) => {
  const raw = {
    'api-key': 'fixture-key',
    ...(value === null ? { 'disable-codex-cloaking': null } : {}),
  };
  const sibling = { 'api-key': 'sibling', 'alpha-search': true };
  const groups = backend('codex', [{ name: 'fixture', keys: [raw, sibling] }]);
  const config = normalizeProviderGroups(groups())[0] as ProviderKeyConfig;
  await providersApi.updateCodexConfig(config.apiKey, undefined, {
    ...config,
    ...pickProviderBehavior(config, 'codex'),
    weight: 3,
  });
  expect(groups()[0].keys).toEqual([{ ...raw, weight: 3 }, sibling]);
});
test('Claude system-message rebuild preserves explicit false and unrelated cloak data', async () => {
  const groups = backend('claude', [
    {
      name: 'fixture',
      keys: [
        { 'api-key': 'fixture-key', 'rebuild-mid-system-message': true, cloak: { mode: 'auto' } },
      ],
    },
  ]);
  const config = normalizeProviderGroups(groups())[0] as ProviderKeyConfig;
  expect(config.rebuildMidSystemMessage).toBe(true);
  await providersApi.updateClaudeConfig(config.apiKey, undefined, {
    ...config,
    rebuildMidSystemMessage: false,
  });
  expect(key(groups())).toEqual({
    'api-key': 'fixture-key',
    'rebuild-mid-system-message': false,
    cloak: { mode: 'auto' },
  });
});
test('OpenAI prompt cache key can be enabled on create and explicitly disabled on edit', async () => {
  const groups = backend('openai-compatibility');
  await providersApi.createOpenAIProvider({
    name: 'fixture',
    baseUrl: 'https://example.invalid',
    apiKeyEntries: [{ apiKey: 'fixture-key' }],
    supportPromptCacheKey: true,
  });
  const config = normalizeProviderGroups(groups(), true)[0] as OpenAIProviderConfig;
  expect(config.supportPromptCacheKey).toBe(true);
  await providersApi.updateOpenAIProvider(config.name, 0, {
    ...config,
    supportPromptCacheKey: false,
  });
  expect(groups()[0]['support-prompt-cache-key']).toBe(false);
  expect(key(groups())).toEqual({ 'api-key': 'fixture-key' });
});
test('UI and form projection gate behavior by actual provider or sponsor protocol', () => {
  const options = {
    alphaSearch: true,
    disableCodexCloaking: false,
    rebuildMidSystemMessage: true,
    supportPromptCacheKey: true,
  };
  expect(pickProviderBehavior(options, 'codex')).toEqual({
    alphaSearch: true,
    disableCodexCloaking: false,
  });
  expect(pickProviderBehavior(options, 'claude')).toEqual({ rebuildMidSystemMessage: true });
  expect(pickProviderBehavior(options, 'openaiCompatibility')).toEqual({
    supportPromptCacheKey: true,
  });
  for (const brand of ['gemini', 'interactions', 'vertex', 'xai', 'meta'] as ProviderBrand[]) {
    expect(pickProviderBehavior(options, brand)).toEqual({});
    expect(
      renderToStaticMarkup(
        createElement(ProviderBehaviorEditor, {
          brand,
          value: options,
          onChange: () => {},
          disabled: false,
        })
      )
    ).toBe('');
  }
  for (const brand of ['codex', 'claude', 'openaiCompatibility'] as ProviderBrand[]) {
    const html = renderToStaticMarkup(
      createElement(ProviderBehaviorEditor, {
        brand,
        value: options,
        onChange: () => {},
        disabled: true,
      })
    );
    expect(html).toContain('disabled=""');
    expect(html).toContain('aria-describedby');
  }
});
test('unsupported behavior does not leak through a non-Codex API serializer', async () => {
  const groups = backend('xai');
  await providersApi.createXAIConfig({
    apiKey: 'fixture-key',
    baseUrl: 'https://example.invalid',
    alphaSearch: true,
    disableCodexCloaking: true,
    rebuildMidSystemMessage: true,
  });
  expect(key(groups())).toEqual({ 'api-key': 'fixture-key' });
});
