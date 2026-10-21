import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { providersApi, type ProviderFamily } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import { mergeDiscoveredModels } from '@/features/providers/modelEntries';
import type { OpenAIProviderConfig, ProviderKeyConfig } from '@/types';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

function backend(family: ProviderFamily, group: Record<string, unknown>) {
  let stored = structuredClone(group);
  const writes: unknown[] = [];
  apiClient.get = (async () => ({
    routing: { cooldown: { 'disable-cooling': true } },
    'api-keys': { [family]: [structuredClone(stored)] },
  })) as typeof apiClient.get;
  apiClient.put = (async (path: string, value: unknown) => {
    expect(path).toBe(`/config/api-keys/${family}`);
    writes.push(value);
    stored = structuredClone((value as Record<string, unknown>[])[0]);
  }) as typeof apiClient.put;
  return { group: () => stored, writes };
}
const row = (group: Record<string, unknown>) =>
  (normalizeProviderGroups([group]) as ProviderKeyConfig[])[0];
const key = (group: Record<string, unknown>) => (group.keys as Record<string, unknown>[])[0];
const openai = (group: Record<string, unknown>) =>
  (normalizeProviderGroups([group], true) as OpenAIProviderConfig[])[0];

const keyApis = [
  ['gemini', providersApi.updateGeminiKey],
  ['interactions', providersApi.updateInteractionsKey],
  ['codex', providersApi.updateCodexConfig],
  ['meta', providersApi.updateMetaConfig],
  ['xai', providersApi.updateXAIConfig],
  ['claude', providersApi.updateClaudeConfig],
] as const;

describe('v8 cooling overrides', () => {
  for (const [family, update] of keyApis) {
    test(`${family} persists explicit false instead of inheriting disabled global cooling`, async () => {
      const b = backend(family, {
        name: 'fixture',
        keys: [{ 'api-key': 'fixture-key', 'disable-cooling': true }],
      });
      const config = row(b.group());
      await update(config.apiKey, config.baseUrl, { ...config, disableCooling: false });
      expect(key(b.group())['disable-cooling']).toBe(false);
      const current = row(b.group());
      await update(current.apiKey, current.baseUrl, { ...current, weight: 7 });
      expect(key(b.group())).toEqual({
        'api-key': 'fixture-key',
        'disable-cooling': false,
        weight: 7,
      });
    });

    test.each([undefined, null])(
      `${family} preserves absent/null inheritance (%s)`,
      async (value) => {
        const rawKey = {
          'api-key': 'fixture-key',
          ...(value === undefined ? {} : { 'disable-cooling': value }),
        };
        const b = backend(family, { name: 'fixture', keys: [rawKey] });
        const config = row(b.group());
        await update(config.apiKey, config.baseUrl, { ...config, weight: 7 });
        expect(key(b.group())).toEqual({ ...rawKey, weight: 7 });
      }
    );
  }

  test('OpenAI group false overrides the global setting and survives unrelated edits', async () => {
    const b = backend('openai-compatibility', {
      name: 'fixture',
      'base-url': 'https://example.invalid',
      'disable-cooling': true,
      keys: [{ 'api-key': 'fixture-key' }],
    });
    const config = openai(b.group());
    await providersApi.updateOpenAIProvider(config.name, 0, { ...config, disableCooling: false });
    expect(b.group()['disable-cooling']).toBe(false);
    const current = openai(b.group());
    await providersApi.updateOpenAIProvider(current.name, 0, { ...current, priority: 3 });
    expect(b.group()['disable-cooling']).toBe(false);
  });

  test('explicit false can override global cooling even without a previous per-key value', async () => {
    const b = backend('codex', { name: 'fixture', keys: [{ 'api-key': 'fixture-key' }] });
    const config = row(b.group());
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      disableCooling: false,
    });
    expect(key(b.group())['disable-cooling']).toBe(false);
  });
});

describe('provider model discovery merge', () => {
  test('retains same-name aliases and source identities while deduplicating discovered names', () => {
    const a = { name: 'shared', alias: 'a', sourceIndex: 0, thinkingJson: '{"levels":["high"]}' };
    const b = { name: 'shared', alias: 'b', sourceIndex: 1 };
    const current = [a, b];
    const merged = mergeDiscoveredModels(current, [
      { name: 'shared' },
      { name: ' fresh ', alias: ' new ' },
      { name: 'fresh' },
    ]);
    expect(merged).toEqual([a, b, { name: 'fresh', alias: 'new' }]);
    expect(merged[0]).toBe(a);
    expect(merged[1]).toBe(b);
    expect(current).toEqual([a, b]);
    expect(merged[2].sourceIndex).toBeUndefined();
  });

  test('only removes empty placeholders and keeps empty discovery unchanged', () => {
    const current = [{ name: '', alias: '' }];
    expect(mergeDiscoveredModels(current, [])).toBe(current);
    expect(mergeDiscoveredModels(current, [{ name: 'fresh' }])).toEqual([
      { name: 'fresh', alias: '' },
    ]);
    expect(mergeDiscoveredModels(current, [{ name: ' ' }])).toEqual(current);
  });
});

describe('v8 model row identity', () => {
  const originalModel = {
    name: 'old',
    alias: 'public',
    'display-name': 'Keep me',
    'max-context-length': 1234,
    'force-mapping': true,
    'is-compat': true,
    'support-configuration-update': false,
    thinking: { levels: ['high'] },
  };

  test.each([false, true])(
    'rename preserves metadata on key/inherited models (%s)',
    async (inherited) => {
      const group = {
        name: 'fixture',
        ...(inherited ? { models: [originalModel] } : {}),
        keys: [{ 'api-key': 'fixture-key', models: inherited ? null : [originalModel] }],
      };
      const b = backend('codex', group);
      const config = row(b.group());
      await providersApi.updateCodexConfig(config.apiKey, undefined, {
        ...config,
        models: [{ ...config.models![0], name: 'new', alias: 'renamed', thinking: undefined }],
      });
      const metadata: Record<string, unknown> = { ...originalModel };
      delete metadata.thinking;
      expect(key(b.group()).models).toEqual([{ ...metadata, name: 'new', alias: 'renamed' }]);
      if (inherited) expect(b.group().models).toEqual([originalModel]);
      expect(JSON.stringify(b.writes)).not.toContain('sourceIndex');
    }
  );

  test('same-name aliases keep their own metadata through reorder, rename and deletion', async () => {
    const a = { name: 'shared', alias: 'a', 'display-name': 'A', 'is-compat': true };
    const bModel = { name: 'shared', alias: 'b', 'display-name': 'B', 'max-context-length': 42 };
    const b = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [a, bModel] }],
    });
    const config = row(b.group());
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      models: [{ ...config.models![1], name: 'renamed' }, config.models![0]],
    });
    expect(key(b.group()).models).toEqual([{ ...bModel, name: 'renamed' }, a]);
    const current = row(b.group());
    await providersApi.updateCodexConfig(current.apiKey, undefined, {
      ...current,
      models: [current.models![0]],
    });
    expect(key(b.group()).models).toEqual([{ ...bModel, name: 'renamed' }]);
  });

  test('OpenAI model rename retains raw metadata and new models do not inherit it', async () => {
    const b = backend('openai-compatibility', {
      name: 'fixture',
      'base-url': 'https://example.invalid',
      models: [originalModel],
      keys: [{ 'api-key': 'fixture-key' }],
    });
    const config = openai(b.group());
    await providersApi.updateOpenAIProvider(config.name, 0, {
      ...config,
      models: [
        { name: 'fresh', alias: 'fresh' },
        { ...config.models![0], name: 'new' },
      ],
    });
    expect(b.group().models).toEqual([
      { name: 'fresh', alias: 'fresh' },
      { ...originalModel, name: 'new' },
    ]);
    expect(JSON.stringify(b.writes)).not.toContain('sourceIndex');
  });

  test('replacing a removed row with a new identical-looking model does not inherit metadata', async () => {
    const b = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [originalModel] }],
    });
    const config = row(b.group());
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      // A new form row contains only the fields the user enters, not a clone of
      // the deleted row's now-editable advanced properties.
      models: [
        {
          name: originalModel.name,
          alias: originalModel.alias,
          thinking: originalModel.thinking,
          sourceIndex: null,
        },
      ],
    });
    expect(key(b.group()).models).toEqual([
      {
        name: originalModel.name,
        alias: originalModel.alias,
        thinking: originalModel.thinking,
      },
    ]);
  });

  test('identical visible models retain their metadata when reordered', async () => {
    const a = { name: 'shared', alias: 'shared', 'display-name': 'A' };
    const bModel = { name: 'shared', alias: 'shared', 'display-name': 'B' };
    const b = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [a, bModel] }],
    });
    const config = row(b.group());
    await providersApi.updateCodexConfig(config.apiKey, undefined, {
      ...config,
      models: [config.models![1], config.models![0]],
    });
    expect(key(b.group()).models).toEqual([bModel, a]);
  });

  test('duplicate model identities reject the save', async () => {
    const b = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [originalModel] }],
    });
    const config = row(b.group());
    await expect(
      providersApi.updateCodexConfig(config.apiKey, undefined, {
        ...config,
        models: [config.models![0], { ...config.models![0], name: 'other' }],
      })
    ).rejects.toThrow();
    expect(b.writes).toHaveLength(0);
  });

  test.each([-1, 99])('invalid model identity %s rejects the save', async (sourceIndex) => {
    const b = backend('codex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [originalModel] }],
    });
    const config = row(b.group());
    await expect(
      providersApi.updateCodexConfig(config.apiKey, undefined, {
        ...config,
        models: [{ ...config.models![0], sourceIndex, name: 'new' }],
      })
    ).rejects.toThrow();
    expect(b.writes).toHaveLength(0);
  });

  test('Vertex same-name alias survives thinking edits and adding other models', async () => {
    const model = { name: 'm', alias: 'm', thinking: { levels: ['high'] } };
    const b = backend('vertex', {
      name: 'fixture',
      keys: [{ 'api-key': 'fixture-key', models: [model] }],
    });
    const config = row(b.group());
    expect(config.models![0].alias).toBe('m');
    await providersApi.updateVertexConfig(config.apiKey, undefined, {
      ...config,
      models: [{ ...config.models![0], thinking: { levels: ['low'] } }],
    });
    const changed = { ...model, thinking: { levels: ['low'] } };
    expect(key(b.group()).models).toEqual([changed]);
    const current = row(b.group());
    await providersApi.updateVertexConfig(current.apiKey, undefined, {
      ...current,
      models: [...current.models!, { name: 'new', alias: 'other' }],
    });
    expect(key(b.group()).models).toEqual([changed, { name: 'new', alias: 'other' }]);
    expect(JSON.stringify(b.writes)).not.toContain('sourceIndex');
  });
});
