import { afterEach, describe, expect, test } from 'bun:test';
import { apiClient } from '../src/services/api/client';
import { providersApi, type ProviderFamily } from '../src/services/api/providers';
import { normalizeConfigResponse, normalizeProviderGroups } from '../src/services/api/transformers';
import type { ProviderKeyConfig, OpenAIProviderConfig } from '../src/types';
import { useAuthStore } from '../src/stores/useAuthStore';

const originalGet = apiClient.get;
const originalPut = apiClient.put;
afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

function backend(family: ProviderFamily, groups: Record<string, unknown>[] = []) {
  let state = structuredClone(groups);
  const writes: unknown[] = [];
  apiClient.get = (async (url: string) => {
    expect(url).toBe('/config');
    return { 'api-keys': { [family]: structuredClone(state) } };
  }) as typeof apiClient.get;
  apiClient.put = (async (url: string, data: unknown) => {
    expect(url).toBe(`/config/api-keys/${family}`);
    writes.push(data);
    state = structuredClone(data as Record<string, unknown>[]);
  }) as typeof apiClient.put;
  return { groups: () => state, writes };
}
const rows = (groups: unknown) => normalizeProviderGroups(groups) as ProviderKeyConfig[];

describe('v8 provider groups', () => {
  test('reads only the v8 tree and retains empty groups and exact source snapshots', () => {
    const group = {
      name: 'team',
      'base-url': 'https://example.invalid',
      priority: 8,
      headers: { A: 'group' },
      keys: [
        { 'api-key': 'fixture-a', priority: null, headers: null },
        { 'api-key': 'fixture-b', priority: 0, headers: {} },
      ],
    };
    const config = normalizeConfigResponse({
      'api-keys': { codex: [group, { name: 'empty', keys: [] }] },
      access: { 'api-keys': ['client-fixture'] },
      observability: { logs: { debug: true } },
      requests: { 'proxy-url': 'direct' },
      routing: { retry: { 'request-retry': 4 } },
      'codex-api-key': [{ 'api-key': 'must-not-read' }],
    });
    expect(config.codexApiKeys).toHaveLength(2);
    expect(config.codexApiKeys?.map((r) => r.priority)).toEqual([8, 0]);
    expect(config.codexApiKeys?.[0].source).toMatchObject({ groupIndex: 0, keyIndex: 0, group });
    expect(config.providerGroups?.codex).toHaveLength(2);
    expect(config.apiKeys).toEqual(['client-fixture']);
    expect(config.debug).toBe(true);
    expect(config.requestRetry).toBe(4);
    expect(config.proxyUrl).toBe('direct');
    expect(
      normalizeConfigResponse({ 'codex-api-key': [{ 'api-key': 'old' }] }).codexApiKeys
    ).toEqual([]);
  });

  test('preserves group policies, explicit nulls, unknown fields and siblings on row edits', async () => {
    const group = {
      name: 'team',
      'base-url': 'https://example.invalid',
      priority: 9,
      'request-retry': 3,
      'request-scoped-errors': ['fixture'],
      headers: { A: 'group' },
      models: [{ name: 'model', alias: 'alias', future: 'keep' }],
      keys: [
        {
          'api-key': 'fixture-a',
          priority: null,
          headers: null,
          models: null,
          weight: 2,
          future: 'keep',
        },
        { 'api-key': 'fixture-b', weight: 4 },
      ],
    };
    const b = backend('codex', [group]);
    const row = rows([group])[0];
    await providersApi.updateCodexConfig(row.apiKey, row.baseUrl, { ...row, weight: 5 });
    expect(b.groups()).toEqual([
      { ...group, keys: [{ ...group.keys[0], weight: 5 }, group.keys[1]] },
    ]);
    const current = rows(b.groups())[0];
    await providersApi.updateCodexConfig(current.apiKey, current.baseUrl, {
      ...current,
      priority: 0,
      models: [{ name: 'model', alias: 'new' }],
    });
    expect((b.groups()[0].keys as Record<string, unknown>[])[0]).toMatchObject({
      priority: 0,
      models: [{ name: 'model', alias: 'new', future: 'keep' }],
    });
    expect(b.groups()[0].models).toEqual(group.models);
  });

  test.each([undefined, null, false])(
    'a weight edit keeps the original WebSocket flag %s',
    async (websockets) => {
      const key = {
        'api-key': 'fixture',
        weight: 3,
        ...(websockets === undefined ? {} : { websockets }),
      };
      const group = { name: 'team', keys: [key] };
      const b = backend('codex', [group]);
      const row = rows([group])[0];
      await providersApi.updateCodexConfig(row.apiKey, row.baseUrl, {
        ...row,
        weight: 5,
        websockets: false,
      });
      expect(b.groups()).toEqual([{ ...group, keys: [{ ...key, weight: 5 }] }]);
    }
  );

  test('clearing inherited collections creates an empty override, not accidental inheritance', async () => {
    const group = {
      name: 'team',
      models: [{ name: 'inherited' }],
      headers: { A: 'a' },
      'disable-cooling': true,
      keys: [{ 'api-key': 'fixture', models: null }],
    };
    const b = backend('codex', [group]);
    const row = rows([group])[0];
    await providersApi.updateCodexConfig(row.apiKey, undefined, {
      ...row,
      models: undefined,
      headers: undefined,
      disableCooling: false,
    });
    expect((b.groups()[0].keys as unknown[])[0]).toEqual({
      'api-key': 'fixture',
      models: [],
      headers: {},
      'disable-cooling': false,
    });
  });

  test('identifies repeated credentials by source, tolerates sibling edits and reordered groups, refuses stale keys', async () => {
    const group = {
      name: 'one',
      keys: [
        { 'api-key': 'same', weight: 1 },
        { 'api-key': 'same', weight: 2 },
      ],
    };
    const other = { name: 'two', keys: [{ 'api-key': 'same', weight: 3 }] };
    const [first, second] = rows([group]);
    const b = backend('codex', [other, group]);
    await providersApi.updateCodexConfig(first.apiKey, undefined, { ...first, weight: 4 });
    await providersApi.deleteCodexConfig(second.apiKey, undefined, second.source);
    expect(b.groups()[1]).toEqual({ ...group, keys: [{ 'api-key': 'same', weight: 4 }] });
    await expect(
      providersApi.updateCodexConfig(first.apiKey, undefined, { ...first, weight: 5 })
    ).rejects.toThrow();
    expect(b.writes).toHaveLength(2);
  });

  test('ambiguous identical groups and keys are rejected without writes', async () => {
    const group = { name: 'duplicate', keys: [{ 'api-key': 'fixture' }] };
    const b = backend('codex', [group, group]);
    const row = rows([group])[0];
    await expect(
      providersApi.deleteCodexConfig(row.apiKey, undefined, row.source)
    ).rejects.toThrow();
    expect(b.writes).toHaveLength(0);
  });

  test('empty/missing sections create valid named groups using list replacement', async () => {
    let written: unknown;
    apiClient.get = (async () => ({})) as typeof apiClient.get;
    apiClient.put = (async (url: string, data: unknown) => {
      expect(url).toBe('/config/api-keys/gemini');
      written = data;
    }) as typeof apiClient.put;
    await providersApi.createGeminiKey({
      apiKey: 'fixture',
      baseUrl: 'https://example.invalid',
      priority: 3,
    });
    expect(written).toEqual([
      {
        name: 'gemini-1',
        'base-url': 'https://example.invalid',
        keys: [{ 'api-key': 'fixture', priority: 3 }],
      },
    ]);
  });

  test('OpenAI keys remain credential entries; toggles and deletion use guarded list writes', async () => {
    const group = {
      name: 'compat',
      'base-url': 'https://example.invalid',
      priority: 5,
      keys: [{ 'api-key': 'fixture', 'proxy-url': null, custom: 'keep', weight: 2 }],
    };
    const b = backend('openai-compatibility', [group]);
    const row = (normalizeProviderGroups([group], true) as OpenAIProviderConfig[])[0];
    await providersApi.updateOpenAIProvider(row.name, 0, {
      ...row,
      apiKeyEntries: [{ ...row.apiKeyEntries[0], weight: 4 }],
    });
    expect(b.groups()).toEqual([{ ...group, keys: [{ ...group.keys[0], weight: 4 }] }]);
    let current = (await providersApi.getOpenAIProviders())[0];
    await providersApi.updateOpenAIProviderDisabled(999, true, current.source);
    current = (await providersApi.getOpenAIProviders())[0];
    await providersApi.deleteOpenAIProvider(999, current.source);
    expect(b.groups()).toEqual([]);
  });

  test('does not write an old response into a switched connection', async () => {
    const getState = useAuthStore.getState;
    const saved = getState();
    let wrote = false;
    apiClient.get = (async () => {
      useAuthStore.getState = () => ({ ...saved, apiBase: 'https://other.invalid' });
      return {};
    }) as typeof apiClient.get;
    apiClient.put = (async () => {
      wrote = true;
    }) as typeof apiClient.put;
    try {
      await expect(providersApi.createCodexConfig({ apiKey: 'fixture' })).rejects.toThrow();
      expect(wrote).toBe(false);
    } finally {
      useAuthStore.getState = getState;
    }
  });

  test('rejects an ABA client connection switch before auth-store login completes', async () => {
    const first = { apiBase: 'https://first.invalid', managementKey: 'fixture' };
    apiClient.setConfig(first);
    let wrote = false;
    apiClient.get = (async () => {
      apiClient.setConfig({ apiBase: 'https://second.invalid', managementKey: 'other' });
      apiClient.setConfig(first);
      return {};
    }) as typeof apiClient.get;
    apiClient.put = (async () => {
      wrote = true;
    }) as typeof apiClient.put;
    try {
      await expect(providersApi.createCodexConfig({ apiKey: 'fixture' })).rejects.toMatchObject({
        name: 'AbortError',
      });
      expect(wrote).toBe(false);
    } finally {
      apiClient.setConfig({ apiBase: '', managementKey: '' });
    }
  });

  for (const [family, create, update, remove] of [
    [
      'interactions',
      providersApi.createInteractionsKey,
      providersApi.updateInteractionsKey,
      providersApi.deleteInteractionsKey,
    ],
    [
      'meta',
      providersApi.createMetaConfig,
      providersApi.updateMetaConfig,
      providersApi.deleteMetaConfig,
    ],
    [
      'xai',
      providersApi.createXAIConfig,
      providersApi.updateXAIConfig,
      providersApi.deleteXAIConfig,
    ],
    [
      'claude',
      providersApi.createClaudeConfig,
      providersApi.updateClaudeConfig,
      providersApi.deleteClaudeConfig,
    ],
    [
      'vertex',
      providersApi.createVertexConfig,
      providersApi.updateVertexConfig,
      providersApi.deleteVertexConfig,
    ],
  ] as const) {
    test(`${family} create/update/delete retains the group and unknown key fields`, async () => {
      const b = backend(family);
      await create({ apiKey: 'fixture', baseUrl: 'https://example.invalid', weight: 3 });
      const groups = b.groups();
      (groups[0].keys as Record<string, unknown>[])[0].future = true;
      const row = rows(groups)[0];
      await update(row.apiKey, row.baseUrl, { ...row, weight: 6 });
      expect((b.groups()[0].keys as Record<string, unknown>[])[0]).toMatchObject({
        weight: 6,
        future: true,
      });
      const current = rows(b.groups())[0];
      await remove(current.apiKey, current.baseUrl, current.source);
      expect(b.groups()[0]).toEqual({
        name: `${family}-1`,
        'base-url': 'https://example.invalid',
        keys: [],
      });
    });
  }
});

test('OpenAI credential rotation retains per-key metadata using source identity', async () => {
  const group = {
    name: 'compat',
    'base-url': 'https://example.invalid',
    keys: [
      { 'api-key': 'first', weight: 2, custom: { keep: true } },
      { 'api-key': 'second', 'proxy-url': null, future: 'keep' },
    ],
  };
  const b = backend('openai-compatibility', [group]);
  const current = (await providersApi.getOpenAIProviders())[0];
  await providersApi.updateOpenAIProvider(current.name, 0, {
    ...current,
    apiKeyEntries: [{ ...current.apiKeyEntries[1], apiKey: 'rotated' }, current.apiKeyEntries[0]],
  });
  expect(b.groups()[0].keys).toEqual([{ ...group.keys[1], 'api-key': 'rotated' }, group.keys[0]]);
});

test('identical group/key rows remain individually editable against the unchanged snapshot', async () => {
  const group = { name: 'same', keys: [{ 'api-key': 'same' }, { 'api-key': 'same' }] };
  const b = backend('codex', [group, group]);
  const row = rows(b.groups())[3];
  await providersApi.updateCodexConfig(row.apiKey, undefined, { ...row, weight: 7 });
  expect(b.groups()[0]).toEqual(group);
  expect(b.groups()[1].keys).toEqual([{ 'api-key': 'same' }, { 'api-key': 'same', weight: 7 }]);
});

test('Claude fingerprint opt-in and clear do not erase unrelated persisted fields', async () => {
  const b = backend('claude');
  await providersApi.createClaudeConfig({
    apiKey: 'fixture',
    fingerprintProfile: 'claude-code-cli',
  });
  const keys = b.groups()[0].keys as Record<string, unknown>[];
  expect(keys[0]['fingerprint-profile']).toBe('claude-code-cli');
  keys[0].future = 'keep';
  const row = rows(b.groups())[0];
  await providersApi.updateClaudeConfig(row.apiKey, undefined, {
    ...row,
    fingerprintProfile: undefined,
  });
  expect(b.groups()[0].keys).toEqual([{ 'api-key': 'fixture', future: 'keep' }]);
});

for (const prepend of [false, true]) {
  test(`OpenAI new same-key credential has independent proxy and metadata (prepend=${prepend})`, async () => {
    const group = {
      name: 'compat',
      'base-url': 'https://example.invalid',
      keys: [{ 'api-key': 'fixture', 'proxy-url': 'direct', weight: 2, custom: 'keep' }],
    };
    const b = backend('openai-compatibility', [group]);
    const current = (await providersApi.getOpenAIProviders())[0];
    const added = { apiKey: 'fixture', proxyUrl: 'http://proxy.invalid', weight: 3 };
    const entries = [current.apiKeyEntries[0], added];
    await providersApi.updateOpenAIProvider(current.name, 0, {
      ...current,
      apiKeyEntries: prepend ? entries.toReversed() : entries,
    });
    const expected = [
      group.keys[0],
      { 'api-key': 'fixture', 'proxy-url': added.proxyUrl, weight: 3 },
    ];
    expect(b.groups()[0].keys).toEqual(prepend ? expected.toReversed() : expected);
    expect(b.writes).toHaveLength(1);
  });
}

test.each([-1, 2, 0.5])(
  'OpenAI rejects invalid sourceIndex %s without writing',
  async (sourceIndex) => {
    const group = {
      name: 'compat',
      'base-url': 'https://example.invalid',
      keys: [{ 'api-key': 'fixture' }],
    };
    const b = backend('openai-compatibility', [group]);
    const current = (await providersApi.getOpenAIProviders())[0];
    await expect(
      providersApi.updateOpenAIProvider(current.name, 0, {
        ...current,
        apiKeyEntries: [{ ...current.apiKeyEntries[0], sourceIndex, weight: 2 }],
      })
    ).rejects.toThrow();
    expect(b.writes).toHaveLength(0);
  }
);

test('OpenAI rejects repeated sourceIndex without writing', async () => {
  const b = backend('openai-compatibility', [
    { name: 'compat', 'base-url': 'https://example.invalid', keys: [{ 'api-key': 'fixture' }] },
  ]);
  const current = (await providersApi.getOpenAIProviders())[0];
  await expect(
    providersApi.updateOpenAIProvider(current.name, 0, {
      ...current,
      apiKeyEntries: [current.apiKeyEntries[0], { ...current.apiKeyEntries[0], weight: 2 }],
    })
  ).rejects.toThrow();
  expect(b.writes).toHaveLength(0);
});

for (const inherited of [false, true]) {
  test(`credential edits preserve auth-index headers (inherited=${inherited})`, async () => {
    const headers = { 'auth-index': 'request-header', Other: 'before' };
    const key = {
      'api-key': 'fixture',
      'auth-index': 'response-metadata',
      headers: inherited ? null : headers,
      models: [{ name: 'model', alias: 'before', 'auth-index': 'opaque-model-field' }],
    };
    const group = { name: 'team', ...(inherited ? { headers } : {}), keys: [key] };
    const b = backend('codex', [group]);
    const row = rows([group])[0];
    await providersApi.updateCodexConfig(row.apiKey, undefined, {
      ...row,
      headers: { ...row.headers, Other: 'after' },
      models: [{ name: 'model', alias: 'after' }],
    });
    expect(b.groups()[0]).toEqual({
      ...group,
      keys: [
        {
          'api-key': 'fixture',
          headers: { ...headers, Other: 'after' },
          models: [{ ...key.models[0], alias: 'after' }],
        },
      ],
    });
  });
}

test('OpenAI group headers retain auth-index while edited credentials drop response metadata', async () => {
  const group = {
    name: 'compat',
    'base-url': 'https://example.invalid',
    headers: { 'auth-index': 'request-header', Other: 'before' },
    keys: [{ 'api-key': 'fixture', 'auth-index': 'response-metadata', custom: 'keep' }],
  };
  const b = backend('openai-compatibility', [group]);
  const current = (await providersApi.getOpenAIProviders())[0];
  await providersApi.updateOpenAIProvider(current.name, 0, {
    ...current,
    headers: { ...current.headers, Other: 'after' },
    apiKeyEntries: [{ ...current.apiKeyEntries[0], weight: 2 }],
  });
  expect(b.groups()[0]).toEqual({
    ...group,
    headers: { ...group.headers, Other: 'after' },
    keys: [{ 'api-key': 'fixture', custom: 'keep', weight: 2 }],
  });
});
