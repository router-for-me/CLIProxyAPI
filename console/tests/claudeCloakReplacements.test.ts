import { afterEach, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { providersApi } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import { buildProviderKeyConfig } from '@/features/providers/useProviderWorkbench';
import type { ProviderEntryFormInput } from '@/features/providers/types';
import type { ProviderKeyConfig } from '@/types';

const words = { ' target ': '', ' ': '  ', literal: '$1' };
const patterns = [
  { pattern: '(?m)^target\\s+', replacement: '' },
  { pattern: ' ', replacement: '  ' },
  { pattern: '(?P<word>target)', replacement: '${word}' },
];
const rawCloak = {
  'sensitive-words-replace': words,
  'sensitive-patterns-replace': patterns,
};
const row = (cloak: unknown) =>
  normalizeProviderGroups([
    { name: 'fixture', keys: [{ 'api-key': 'fixture-key', cloak }] },
  ])[0] as ProviderKeyConfig;
const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

test('Claude cloak normalization preserves deletion, whitespace, RE2 syntax and rule order', () => {
  expect(row(rawCloak).cloak).toEqual({
    sensitiveWordsReplace: words,
    sensitivePatternsReplace: patterns,
  });
  expect(row({}).cloak).toBeUndefined();
  expect(
    row({
      'sensitive-words-replace': { ...words, invalid: null, number: 1 },
      'sensitive-patterns-replace': [...patterns, null, {}, { pattern: 'x', replacement: 1 }],
    }).cloak
  ).toEqual(row(rawCloak).cloak);
});

test('Claude form builder and API round trip replacements, unrelated edits and explicit clearing', async () => {
  let stored: Record<string, unknown> = {
    name: 'fixture',
    keys: [{ 'api-key': 'fixture-key', cloak: rawCloak }],
  };
  apiClient.get = (async () => ({ 'api-keys': { claude: [stored] } })) as typeof apiClient.get;
  apiClient.put = (async (path: string, groups: Record<string, unknown>[]) => {
    expect(path).toBe('/config/api-keys/claude');
    stored = structuredClone(groups[0]);
  }) as typeof apiClient.put;
  const input = {
    apiKey: '',
    name: '',
    prefix: '',
    baseUrl: '',
    proxyUrl: '',
    headers: [],
    models: [],
    excludedModelsText: '',
    disabled: false,
    cloak: {
      mode: '',
      strictMode: false,
      sensitiveWordsText: '',
      cacheUserId: false,
      sensitiveWordsReplace: words,
      sensitivePatternsReplace: patterns,
    },
  } satisfies ProviderEntryFormInput;
  const config = buildProviderKeyConfig('claude', input, row(rawCloak)) as ProviderKeyConfig;
  expect(config.cloak?.sensitiveWordsReplace).toEqual(words);
  expect(config.cloak?.sensitivePatternsReplace).toEqual(patterns);
  await providersApi.updateClaudeConfig(config.apiKey, undefined, config);
  const readBack = () => normalizeProviderGroups([stored])[0] as ProviderKeyConfig;
  expect(readBack().cloak).toMatchObject({
    sensitiveWordsReplace: words,
    sensitivePatternsReplace: patterns,
  });
  await providersApi.updateClaudeConfig(config.apiKey, undefined, { ...readBack(), priority: 7 });
  expect(readBack().cloak?.sensitivePatternsReplace).toEqual(patterns);
  await providersApi.updateClaudeConfig(config.apiKey, undefined, {
    ...readBack(),
    cloak: { ...readBack().cloak, sensitiveWordsReplace: {}, sensitivePatternsReplace: [] },
  });
  expect(readBack().cloak).toMatchObject({
    sensitiveWordsReplace: {},
    sensitivePatternsReplace: [],
  });
});
