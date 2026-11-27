import { expect, test } from 'bun:test';
import {
  buildNativeConnectionMembers,
  buildProviderGroups,
} from '@/features/providers/useProviderWorkbench';
import type { ProviderEntryFormInput } from '@/features/providers/types';
import { normalizeConfigResponse } from '@/services/api/transformers';

const input: ProviderEntryFormInput = {
  apiKey: '',
  name: '',
  baseUrl: '',
  prefix: '',
  proxyUrl: '',
  disabled: false,
  models: [],
  headers: [],
  excludedModelsText: '',
};

test('one connection draft preserves invisible and untouched keys, removes omitted visible rows and maps edits to source indices', () => {
  const rawGroup = {
    keys: [
      { 'api-key': 'fixture-removed' },
      { future: true },
      { 'api-key': 'fixture-edit' },
      { 'api-key': 'fixture-unchanged' },
    ],
  };
  const config = normalizeConfigResponse({ 'api-keys': { gemini: [rawGroup] } });
  const section = buildProviderGroups(config).find((group) => group.id === 'gemini')!
    .nativeSections![0];
  const result = buildNativeConnectionMembers('gemini', section, [
    { resource: section.members[1], input: { ...input, weight: 7 } },
    { resource: section.members[2], input, unchanged: true },
    { resource: null, input: { ...input, apiKey: 'fixture-new' } },
  ]);
  expect(result.map((item) => item.originalKeyIndex)).toEqual([1, 2, 3, undefined]);
  expect(result[0]).toEqual({ originalKeyIndex: 1 });
  expect(result[1].config).toMatchObject({ apiKey: 'fixture-edit', weight: 7 });
  expect(result[2]).toEqual({ originalKeyIndex: 3 });
  expect(result[3].config?.apiKey).toBe('fixture-new');
  expect(buildNativeConnectionMembers('gemini', section, [])).toEqual([{ originalKeyIndex: 1 }]);
});

test('new connections can start empty or with multiple credentials in one save', () => {
  expect(buildNativeConnectionMembers('gemini', undefined, [])).toEqual([]);
  const members = buildNativeConnectionMembers(
    'gemini',
    undefined,
    ['fixture-a', 'fixture-b'].map((apiKey) => ({ resource: null, input: { ...input, apiKey } }))
  );
  expect(members.map((row) => row.config?.apiKey)).toEqual(['fixture-a', 'fixture-b']);
  expect(members.every((row) => row.originalKeyIndex === undefined)).toBe(true);
});

test('credential rotation and additions do not materialize the form’s optional defaults', () => {
  const group = { keys: [{ 'api-key': 'fixture-old' }], 'request-retry': 3 };
  const config = normalizeConfigResponse({ 'api-keys': { codex: [group] } });
  const section = buildProviderGroups(config).find((family) => family.id === 'codex')!
    .nativeSections![0];
  const policy = { retry: '', cooling: 'inherit', errorsMode: 'inherit', errorRules: [] } as const;
  const initialInput: ProviderEntryFormInput = {
    ...input,
    websockets: false,
    runtimePolicy: { ...policy, errorRules: [] },
  };
  const keys = buildNativeConnectionMembers(
    'codex',
    section,
    [section.members[0], null].map((resource) => ({
      resource,
      initialInput,
      input: { ...initialInput, apiKey: 'fixture-rotated' },
    }))
  );
  for (const { config } of keys) {
    expect(config?.apiKey).toBe('fixture-rotated');
    expect(config?.websockets).toBeUndefined();
    expect(config?.inheritFields).toBeUndefined();
  }
  expect(keys[0].config?.requestRetry).toBe(3);
  expect(keys[1].config?.requestRetry).toBeUndefined();
  const [explicit] = buildNativeConnectionMembers('codex', section, [
    {
      resource: section.members[0],
      initialInput,
      input: { ...initialInput, runtimePolicy: { ...policy, retry: '3', errorRules: [] } },
    },
  ]);
  expect(explicit.config?.requestRetry).toBe(3);
  expect(explicit.config?.inheritFields).toEqual(['disable-cooling', 'request-scoped-errors']);
});

test('on/off switches travel with their key without marking it edited', () => {
  const config = normalizeConfigResponse({
    'api-keys': { gemini: [{ keys: [{ 'api-key': 'fixture-a' }, { 'api-key': 'fixture-b' }] }] },
  });
  const section = buildProviderGroups(config).find((group) => group.id === 'gemini')!
    .nativeSections![0];
  const members = buildNativeConnectionMembers('gemini', section, [
    { resource: section.members[0], input, unchanged: true, disabled: true },
    { resource: section.members[1], input, unchanged: true },
  ]);
  expect(members).toEqual([{ originalKeyIndex: 0, disabled: true }, { originalKeyIndex: 1 }]);
});
