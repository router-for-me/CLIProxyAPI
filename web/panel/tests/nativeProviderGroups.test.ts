import { afterEach, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import {
  normalizeNativeProviderGroup,
  providersApi,
  type NativeProviderFamily,
} from '@/services/api/providers';
import {
  buildSubmittedMember,
  memberFields,
  readMemberForm,
  readSharedForm,
  setMemberMode,
  nativeSwitchesDirty,
} from '@/features/providers/nativeConnectionForm';
import {
  buildNativeConnectionMembers,
  buildProviderGroups,
} from '@/features/providers/useProviderWorkbench';
import { normalizeConfigResponse, normalizeProviderGroups } from '@/services/api/transformers';
import type { ProviderEntryFormInput } from '@/features/providers/types';
import type { ProviderKeyConfig } from '@/types';

const { get, getRaw, put, captureConnection } = apiClient;
afterEach(() => Object.assign(apiClient, { get, getRaw, put, captureConnection }));

type Group = Record<string, unknown>;
function backend(family: NativeProviderFamily, initial: Group[] = []) {
  apiClient.captureConnection = () => apiClient;
  let groups = structuredClone(initial);
  let version = 1;
  const data = () => ({ 'api-keys': { [family]: structuredClone(groups) } });
  apiClient.get = (async () => data()) as typeof get;
  apiClient.getRaw = (async (url) => {
    expect(url).toBe('/config');
    return { data: data(), headers: { etag: `"v${version}"` } };
  }) as typeof getRaw;
  apiClient.put = (async (url, value, options) => {
    expect(url).toBe(`/config/api-keys/${family}`);
    if (options) expect(options.headers).toEqual({ 'If-Match': `"v${version}"` });
    groups = structuredClone(value as Group[]);
    version++;
  }) as typeof put;
  const source = (index = 0) => ({ groupIndex: index, groups, group: groups[index] });
  return { groups: () => groups, source, writes: () => version - 1 };
}

test('saving new keys in a disabled group preserves the displayed state with per-key exclusions', async () => {
  const group = { 'excluded-models': ['*'], keys: [] };
  const b = backend('gemini', [group]);
  const section = buildProviderGroups(
    normalizeConfigResponse({ 'api-keys': { gemini: [group] } })
  ).find((family) => family.id === 'gemini')!.nativeSections![0];
  const initial = {
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
  const shared = readSharedForm(group, initial);
  const rows = [
    { apiKey: 'fixture-off', excludedModelsText: 'custom' },
    { apiKey: 'fixture-inherit', excludedModelsText: '' },
    { apiKey: 'fixture-on', excludedModelsText: 'custom', disabled: false },
  ].map(({ apiKey, excludedModelsText, disabled }) =>
    buildSubmittedMember(
      { resource: null, initial, input: { ...initial, apiKey, excludedModelsText }, disabled },
      false,
      shared.disabled
    )
  );
  await providersApi.saveNativeConnection(
    'gemini',
    section.source,
    group,
    buildNativeConnectionMembers('gemini', section, rows)
  );
  expect(b.groups()).toEqual([
    {
      ...group,
      keys: [
        { 'api-key': 'fixture-off', 'excluded-models': ['custom', '*'] },
        { 'api-key': 'fixture-inherit' },
        { 'api-key': 'fixture-on', 'excluded-models': ['custom'] },
      ],
    },
  ]);
});

test('member intent survives save and readback: pin, clear, restore, preserve untouched empty maps/lists', async () => {
  const defaults = {
    prefix: 'team',
    priority: 7,
    'proxy-url': 'direct',
    models: [{ name: 'shared' }],
    headers: { 'X-Fixture': 'yes' },
    'excluded-models': ['old'],
  };
  const opaque = { 'connection-pool': {}, 'request-scoped-errors': [], future: {} };
  const b = backend('codex', [
    { ...defaults, keys: [{ 'api-key': 'fixture', ...opaque, headers: null }] },
  ]);
  const blank: ProviderEntryFormInput = {
    apiKey: '',
    name: '',
    baseUrl: '',
    prefix: '',
    priority: undefined,
    proxyUrl: '',
    models: [],
    headers: [],
    excludedModelsText: '',
    disabled: false,
  };
  const save = async (
    edit: (initial: ProviderEntryFormInput) => ProviderEntryFormInput,
    create = false
  ) => {
    const section = buildProviderGroups(
      normalizeConfigResponse({ 'api-keys': { codex: b.groups() } })
    ).find((f) => f.id === 'codex')!.nativeSections![0];
    const saved = section.members[0];
    const key = saved.raw as ProviderKeyConfig;
    const resource = create ? null : saved;
    const initial = readMemberForm(
      {
        ...blank,
        prefix: key.prefix ?? '',
        proxyUrl: key.proxyUrl ?? '',
        priority: key.priority,
        models: key.models ?? [],
        excludedModelsText: key.excludedModels?.join('\n') ?? '',
      },
      resource
    );
    const input = edit(initial);
    const rows = [
      ...(create ? [{ resource: saved, input: blank, unchanged: true }] : []),
      buildSubmittedMember({ resource, initial, input }, false, false),
    ];
    await providersApi.saveNativeConnection(
      'codex',
      section.source,
      b.source().group,
      buildNativeConnectionMembers('codex', section, rows)
    );
    return (b.groups()[0].keys as Group[]).at(-1)!;
  };
  const clear = (initial: ProviderEntryFormInput) =>
    memberFields.reduce(
      (form, field) => setMemberMode(form, field, field === 'priority' ? 'override' : 'empty'),
      initial
    );
  const empty = {
    prefix: '',
    priority: 0,
    'proxy-url': '',
    headers: {},
    models: [],
    'excluded-models': [],
  };
  expect(
    await save((initial) => {
      expect(Object.values(initial.memberModes!)).toEqual(memberFields.map(() => 'inherit'));
      return {
        ...memberFields.reduce((form, field) => setMemberMode(form, field, 'override'), initial),
        prefix: 'team',
        priority: 7,
        proxyUrl: 'direct',
        headers: [{ key: 'X-Fixture', value: 'yes' }],
        models: [{ name: 'shared' }],
        excludedModelsText: 'old',
      };
    })
  ).toEqual({ 'api-key': 'fixture', ...opaque, ...defaults });
  expect(await save((initial) => ({ ...clear(initial), priority: 0 }))).toEqual({
    'api-key': 'fixture',
    ...opaque,
    ...empty,
  });
  expect(await save((initial) => ({ ...initial, weight: 2 }))).toEqual({
    'api-key': 'fixture',
    ...opaque,
    ...empty,
    weight: 2,
  });
  expect(
    await save((initial) =>
      memberFields.reduce((form, field) => setMemberMode(form, field, 'inherit'), initial)
    )
  ).toEqual({ 'api-key': 'fixture', ...opaque, weight: 2 });
  expect(await save((initial) => ({ ...clear(initial), apiKey: 'fixture-new' }), true)).toEqual({
    'api-key': 'fixture-new',
    ...empty,
  });
});

test('enabling individually disabled members is dirty even when the group default is enabled', () => {
  const group = {
    keys: [
      { 'api-key': 'fixture', 'excluded-models': ['*'] },
      { 'api-key': 'fixture-2', 'excluded-models': ['*'] },
    ],
  };
  const section = buildProviderGroups(
    normalizeConfigResponse({ 'api-keys': { codex: [group] } })
  ).find((f) => f.id === 'codex')!.nativeSections![0];
  const rows = section.members.map((resource) => {
    const initial = readMemberForm(
      {
        apiKey: '',
        name: '',
        baseUrl: '',
        prefix: '',
        proxyUrl: '',
        models: [],
        headers: [],
        excludedModelsText: '',
        disabled: true,
      },
      resource
    );
    return { resource, initial, input: initial };
  });
  expect(nativeSwitchesDirty(rows, false)).toBe(false);
  expect(nativeSwitchesDirty(rows, false, false)).toBe(true);
});

for (const family of [
  'codex',
  'claude',
  'gemini',
  'interactions',
  'vertex',
  'xai',
  'meta',
] as const) {
  test(`${family}: create empty group, append sparse credential, delete group`, async () => {
    const other = { name: 'same', keys: [], future: [1] };
    const b = backend(family, [other]);
    const config = { name: 'same', baseUrl: 'https://fixture.invalid', priority: 2, weight: 99 };
    await providersApi.createNativeGroup(family, config);
    const created = { name: 'same', 'base-url': config.baseUrl, priority: 2, keys: [] };
    expect(b.groups()).toEqual([other, created]);
    await providersApi.appendNativeGroupKey(family, b.source(1), {
      apiKey: 'fixture',
      baseUrl: 'https://ignored.invalid',
      weight: 0,
      requestRetry: 4,
      inheritFields: ['request-retry'],
    });
    expect(b.groups()).toEqual([
      other,
      { ...created, keys: [{ 'api-key': 'fixture', weight: 0 }] },
    ]);
    await providersApi.deleteNativeGroup(family, b.source(1));
    expect(b.groups()).toEqual([other]);
  });
}

test('group edits preserve member overrides, raw policy and model row metadata after reordering', async () => {
  const models = [
    { name: 'old', alias: 'a', future: 1 },
    { name: 'old', alias: 'b', future: 2 },
  ];
  const target = {
    name: 'same',
    keys: [{ 'api-key': 'fixture', headers: null, models: [], future: true }],
    'base-url': 'old',
    'connection-pool': { enabled: true },
    priority: null,
    models,
  };
  const other = { name: 'same', keys: [] };
  const b = backend('codex', [other, target]);
  const config = normalizeNativeProviderGroup(target);
  await providersApi.updateNativeGroup(
    'codex',
    { groupIndex: 0, groups: [target, other], group: target },
    {
      ...config,
      name: 'renamed',
      baseUrl: 'new',
      models: [{ ...config.models![1], name: 'new' }],
    }
  );
  expect(b.groups()).toEqual([
    other,
    {
      ...target,
      name: 'renamed',
      'base-url': 'new',
      models: [{ ...models[1], name: 'new' }],
    },
  ]);
  const row = (normalizeProviderGroups(b.groups()) as ProviderKeyConfig[])[0];
  await providersApi.updateCodexConfig(row.apiKey, row.baseUrl, {
    ...row,
    baseUrl: 'updated-endpoint',
    priority: 3,
  });
  expect(b.groups()[1]['base-url']).toBe('updated-endpoint');
  expect(b.groups()[1].keys).toEqual([{ ...target.keys[0], priority: 3 }]);
});

test('stale, ambiguous and member sources cannot change a group', async () => {
  const group = { name: 'same', keys: [] };
  const b = backend('gemini', [group, group]);
  const stale = { groupIndex: 0, group: { ...group, keys: [{ 'api-key': 'removed' }] } };
  await expect(providersApi.deleteNativeGroup('gemini', stale)).rejects.toThrow();
  await expect(
    providersApi.updateNativeGroup('gemini', { groupIndex: 0, group }, { name: 'other' })
  ).rejects.toThrow();
  await expect(
    providersApi.appendNativeGroupKey(
      'gemini',
      { ...b.source(), keyIndex: 0 },
      { apiKey: 'fixture' }
    )
  ).rejects.toThrow();
  expect(b.writes()).toBe(0);
});

test('never retries a conditional write against a changed configuration', async () => {
  const b = backend('codex');
  let writes = 0;
  const error = Object.assign(new Error('precondition failed'), { status: 412 });
  apiClient.put = async (_url, _value, options) => {
    writes++;
    expect(options?.headers).toEqual({ 'If-Match': '"v1"' });
    throw error;
  };
  await expect(providersApi.createNativeGroup('codex', { name: 'new' })).rejects.toBe(error);
  expect(writes).toBe(1);
  expect(b.groups()).toEqual([]);
});

test('wire editor only replaces supported group settings, never members or unknown metadata', async () => {
  const group = {
    name: 'before',
    keys: [{ 'api-key': 'fixture', headers: null }],
    future: ['opaque'],
    'connection-pool': {
      enabled: false,
      'idle-conn-timeout': '10s',
      'max-idle-conns-per-host': -1,
    },
    models: [
      { name: 'same', alias: 'a', metadata: 1 },
      { name: 'same', alias: 'b', metadata: 2 },
    ],
  };
  const b = backend('codex', [group]);
  const edited = {
    ...group,
    keys: [],
    future: 'wrong',
    models: group.models.toReversed(),
    'connection-pool': {},
  };
  await providersApi.updateRawNativeGroup('codex', b.source(), edited);
  expect(b.groups()).toEqual([{ ...group, models: edited.models, 'connection-pool': {} }]);
  await providersApi.createRawNativeGroup('codex', group);
  expect(b.groups()[1]).toEqual({
    name: group.name,
    keys: [],
    models: group.models,
    'connection-pool': group['connection-pool'],
  });
  await providersApi.updateRawNativeGroup('codex', b.source(1), { name: 'defaults' });
  expect(b.groups()[1]).toEqual({ name: 'defaults', keys: [] });
});

test('a missing revision or mid-read connection switch cannot write a group', async () => {
  const b = backend('codex');
  apiClient.getRaw = (async () => ({ data: {}, headers: {} })) as typeof getRaw;
  await expect(providersApi.createRawNativeGroup('codex', {})).rejects.toThrow();
  const first = { apiBase: 'https://first.invalid', managementKey: 'fixture' };
  apiClient.setConfig(first);
  apiClient.getRaw = (async () => {
    apiClient.setConfig({ apiBase: 'https://second.invalid', managementKey: 'other' });
    apiClient.setConfig(first);
    return { data: {}, headers: { etag: '"v1"' } };
  }) as typeof getRaw;
  try {
    await expect(providersApi.createRawNativeGroup('codex', {})).rejects.toMatchObject({
      name: 'AbortError',
    });
    expect(b.writes()).toBe(0);
  } finally {
    apiClient.setConfig({ apiBase: '', managementKey: '' });
  }
});

test('normalized group edits serialize pool overrides while preserving unknown pool metadata', async () => {
  const original = {
    name: 'fixture',
    keys: [],
    'connection-pool': { enabled: false, future: true, 'max-idle-conns-per-host': -1 },
  };
  const b = backend('gemini', [original]);
  const config = normalizeNativeProviderGroup(original);
  await providersApi.updateNativeGroup('gemini', b.source(), {
    ...config,
    connectionPool: { ...config.connectionPool, idleConnTimeout: '10s' },
  });
  expect(b.groups()).toEqual([
    {
      ...original,
      'connection-pool': { ...original['connection-pool'], 'idle-conn-timeout': '10s' },
    },
  ]);
});

test('creation saves an optional first credential atomically and returns its exact connection', async () => {
  const other = { name: 'Duplicate name', keys: [] };
  const b = backend('gemini', [other]);
  const created = await providersApi.createRawNativeGroup('gemini', other, '  fixture-first  ');
  expect(b.writes()).toBe(1);
  expect(b.groups()).toEqual([other, { ...other, keys: [{ 'api-key': 'fixture-first' }] }]);
  expect(created).toEqual(b.source(1));
  const appended = await providersApi.appendNativeGroupKey('gemini', created, { apiKey: 'second' });
  expect(appended).toEqual(b.source(1));
  const edited = await providersApi.updateGeminiKey('second', undefined, {
    apiKey: 'edited',
    source: { ...appended, keyIndex: 1 },
  });
  expect(edited).toEqual(b.source(1));
  const removed = await providersApi.deleteGeminiKey('fixture-first', undefined, {
    ...edited,
    keyIndex: 0,
  });
  expect(removed).toEqual(b.source(1));
  expect(b.groups()).toEqual([other, { ...other, keys: [{ 'api-key': 'edited' }] }]);
});

test('single save updates defaults and reconciles duplicate member secrets by original index', async () => {
  const group = {
    name: 'old',
    priority: 5,
    'request-retry': 2,
    'connection-pool': { enabled: true },
    keys: [
      { 'api-key': 'fixture', priority: 5, opaque: 0 },
      { 'api-key': 'fixture', priority: null, 'request-retry': null, opaque: 1 },
      { 'api-key': 'hidden-fixture', opaque: { nested: [null] } },
      { 'api-key': 'delete-fixture' },
    ],
  };
  const sibling = { name: 'old', keys: [] };
  const b = backend('codex', [group, sibling]);
  const rows = normalizeProviderGroups([group]) as ProviderKeyConfig[];
  let reads = 0;
  const read = apiClient.getRaw;
  apiClient.getRaw = (...args) => {
    reads++;
    return read(...args);
  };
  const write = apiClient.put;
  apiClient.put = (...args) => {
    expect(args[2]?.headers).toEqual({ 'If-Match': '"v1"' });
    return write(...args);
  };
  const result = await providersApi.saveNativeConnection(
    'codex',
    b.source(),
    {
      ...group,
      keys: [],
      name: 'renamed',
      priority: 42,
    },
    [
      {
        originalKeyIndex: 1,
        config: { ...rows[1], apiKey: 'rotated-fixture', inheritFields: ['request-retry'] },
      },
      { originalKeyIndex: 2 },
      { originalKeyIndex: 0, config: rows[0] },
      { config: { apiKey: 'new-fixture', baseUrl: 'ignored' } },
    ]
  );
  expect(result).toEqual(b.source());
  expect(b.groups()).toEqual([
    {
      ...group,
      name: 'renamed',
      priority: 42,
      keys: [
        { ...group.keys[1], 'api-key': 'rotated-fixture' },
        group.keys[2],
        group.keys[0],
        { 'api-key': 'new-fixture' },
      ],
    },
    sibling,
  ]);
  expect([reads, b.writes()]).toEqual([1, 1]);
});

test('single save supports creating multiple credentials and removing the last credential', async () => {
  const b = backend('vertex');
  const source = await providersApi.saveNativeConnection('vertex', undefined, { name: 'new' }, [
    { config: { apiKey: 'fixture-a', openapi: true } },
    { config: { apiKey: 'fixture-b' } },
  ]);
  expect(b.groups()).toEqual([
    { name: 'new', keys: [{ 'api-key': 'fixture-a', openapi: true }, { 'api-key': 'fixture-b' }] },
  ]);
  await providersApi.saveNativeConnection('vertex', source, source.group, []);
  expect(b.groups()).toEqual([{ name: 'new', keys: [] }]);
  expect(b.writes()).toBe(2);
});

test('single save rejects stale group snapshots and invalid member identity before writing', async () => {
  const group = { name: 'test', keys: [{ 'api-key': 'fixture' }] };
  const b = backend('claude', [group]);
  for (const members of [
    [{ originalKeyIndex: 0 }, { originalKeyIndex: 0 }],
    [{ originalKeyIndex: 1 }],
    [{}],
  ]) {
    await expect(
      providersApi.saveNativeConnection('claude', b.source(), group, members)
    ).rejects.toThrow();
  }
  await expect(
    providersApi.saveNativeConnection(
      'claude',
      { groupIndex: 0, group: { ...group, keys: [] } },
      group,
      []
    )
  ).rejects.toThrow();
  expect(b.writes()).toBe(0);
});

test('atomic connection save never publishes or replays a late write on a new session', async () => {
  const b = backend('gemini');
  const pinned = captureConnection.call(apiClient);
  pinned.getRaw = apiClient.getRaw;
  const write = apiClient.put;
  pinned.put = async (...args) => {
    const result = await write(...args);
    apiClient.setConfig({ apiBase: 'https://next.invalid', managementKey: 'fixture' });
    return result;
  };
  apiClient.captureConnection = () => pinned;
  apiClient.put = () => {
    throw new Error('replayed on mutable client');
  };
  try {
    await expect(
      providersApi.saveNativeConnection('gemini', undefined, { name: 'new' }, [])
    ).rejects.toMatchObject({ name: 'AbortError' });
    expect(b.writes()).toBe(1);
  } finally {
    apiClient.setConfig({ apiBase: '', managementKey: '' });
  }
});

test('moving a partner endpoint persists on the group without rerouting sibling credentials', async () => {
  const group = {
    name: 'combined',
    'base-url': 'https://api.moonshot.cn/v1',
    priority: 5,
    headers: { Shared: 'fixture' },
    keys: [{ 'api-key': 'fixture-a', priority: null }, { 'api-key': 'fixture-b' }],
  };
  const b = backend('codex', [group]);
  const read = () => normalizeConfigResponse({ 'api-keys': { codex: b.groups() } }).codexApiKeys!;
  const move = async (row: ProviderKeyConfig, baseUrl: string) =>
    providersApi.updateCodexConfig(row.apiKey, row.baseUrl, { ...row, baseUrl });
  const target = 'https://api.moonshot.ai/v1';
  await move(read()[0], target);
  expect(b.groups()).toEqual([
    { ...group, keys: [group.keys[1]] },
    { ...group, name: 'codex-2', 'base-url': target, keys: [group.keys[0]] },
  ]);
  expect(read().map((row) => row.baseUrl)).toEqual([group['base-url'], target]);
  await move(read()[1], 'https://elsewhere.invalid');
  expect(read().map((row) => row.baseUrl)).toEqual([
    group['base-url'],
    'https://elsewhere.invalid',
  ]);
  expect(b.groups()).toHaveLength(2);
});

test('a native multi-key Kimi connection remains native and editable after save and reload', async () => {
  const b = backend('codex');
  const settings = { name: 'Kimi pool', 'base-url': 'https://api.moonshot.ai/v1' };
  const reload = () =>
    buildProviderGroups(normalizeConfigResponse({ 'api-keys': { codex: b.groups() } }));
  await providersApi.saveNativeConnection(
    'codex',
    undefined,
    settings,
    ['fixture-a', 'fixture-b'].map((apiKey) => ({ config: { apiKey } }))
  );
  let view = reload();
  const section = view.find((g) => g.id === 'codex')!.nativeSections![0];
  expect(section.members).toHaveLength(2);
  expect(view.find((g) => g.id === 'kimi')!.resources).toHaveLength(0);
  await providersApi.saveNativeConnection('codex', section.source, settings, [
    { originalKeyIndex: 0 },
    { originalKeyIndex: 1, config: { apiKey: 'fixture-rotated' } },
  ]);
  view = reload();
  expect(view.find((g) => g.id === 'codex')!.resources.map((r) => r.apiKey)).toEqual([
    'fixture-a',
    'fixture-rotated',
  ]);
  expect(view.find((g) => g.id === 'kimi')!.resources).toEqual([]);
});

for (const family of ['gemini', 'interactions', 'claude', 'codex', 'xai'] as const) {
  test(`${family}: edit an endpoint-only member and delete it without confusing [{}] with []`, async () => {
    const settings = { name: 'external', 'base-url': 'https://api.qnaigc.com' };
    const b = backend(family, [
      { ...settings, keys: [{}] },
      { name: 'empty', keys: [] },
    ]);
    const reload = () =>
      buildProviderGroups(normalizeConfigResponse({ 'api-keys': { [family]: b.groups() } })).find(
        (g) => g.id === family
      )!.nativeSections!;
    const [section, empty] = reload();
    expect(section.members).toHaveLength(1);
    expect(empty.members).toEqual([]);
    const row = section.members[0].raw as ProviderKeyConfig;
    expect(row.apiKey).toBe('');
    await expect(
      providersApi.saveNativeConnection(family, section.source, { name: 'external' }, [
        { originalKeyIndex: 0 },
      ])
    ).rejects.toThrow('baseUrlRequired');
    await providersApi.saveNativeConnection(family, section.source, settings, [
      { originalKeyIndex: 0, config: { ...row, priority: 3 } },
    ]);
    expect(b.groups()[0].keys).toEqual([{ priority: 3 }]);
    expect(reload()[0].members[0].priority).toBe(3);
    await providersApi.saveNativeConnection(family, reload()[0].source, settings, []);
    expect(reload().map((s) => s.members.length)).toEqual([0, 0]);
    expect(b.groups()[0].keys).toEqual([]);
  });
}

test('Meta and Vertex do not expose blank API-key members', () => {
  const groups = [{ 'base-url': 'https://external.invalid', keys: [{}] }];
  const config = normalizeConfigResponse({
    'api-keys': { meta: groups, vertex: groups, gemini: [{ keys: [{}] }] },
  });
  expect(config.metaApiKeys).toEqual([]);
  expect(config.vertexApiKeys).toEqual([]);
  expect(config.geminiApiKeys).toEqual([]);
});

test('a partner bundle claims only representable protocols across native and compatibility views', () => {
  const group = {
    name: 'Kimi',
    'base-url': 'https://api.moonshot.ai/v1',
    keys: [{ 'api-key': 'fixture-a' }],
  };
  const config = normalizeConfigResponse({
    'api-keys': {
      codex: [group, { ...group, keys: [{ 'api-key': 'fixture-b' }] }],
      'openai-compatibility': [{ ...group, keys: [...group.keys, { 'api-key': 'fixture-c' }] }],
      claude: [{ ...group, 'base-url': 'https://api.moonshot.ai/anthropic' }],
    },
  });
  const view = buildProviderGroups(config);
  expect(view.find((g) => g.id === 'codex')!.nativeSections).toHaveLength(2);
  expect(view.find((g) => g.id === 'openaiCompatibility')!.resources).toHaveLength(1);
  expect(view.find((g) => g.id === 'claude')!.nativeSections).toHaveLength(0);
  const bundle = view.find((g) => g.id === 'kimi')!.resources;
  expect(bundle).toHaveLength(1);
  expect(bundle[0].raw).toMatchObject({
    codex: [],
    openai: [],
    claude: [{ config: config.claudeApiKeys![0] }],
  });
});
