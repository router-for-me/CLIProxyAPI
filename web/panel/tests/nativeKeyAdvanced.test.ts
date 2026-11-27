import { afterEach, expect, test } from 'bun:test';
import { apiClient } from '@/services/api/client';
import { providersApi, type ProviderFamily } from '@/services/api/providers';
import { normalizeProviderGroups } from '@/services/api/transformers';
import { validateConnectionPool } from '@/features/providers/validateConnectionPool';
import { readModelOptions, buildModelOptions } from '@/features/providers/modelOptions';
import type { ProviderKeyConfig } from '@/types';

const { get, put } = apiClient;
afterEach(() => {
  apiClient.get = get;
  apiClient.put = put;
});
function backend(family: ProviderFamily, group: Record<string, unknown>) {
  let current = group;
  apiClient.get = (async () => ({ 'api-keys': { [family]: [current] } })) as typeof get;
  apiClient.put = (async (path: string, groups: Record<string, unknown>[]) => {
    expect(path).toBe(`/config/api-keys/${family}`);
    current = structuredClone(groups[0]);
  }) as typeof put;
  return {
    row: () => normalizeProviderGroups([current])[0] as ProviderKeyConfig,
    key: () => (current.keys as Record<string, unknown>[])[0],
    group: () => current,
  };
}

test('native key pool overrides remain sparse; an empty block opts in and null restores inheritance', async () => {
  const parent = { enabled: true, 'idle-conn-timeout': '5m', 'max-idle-conns-per-host': 2 };
  const b = backend('gemini', {
    name: 'fixture',
    'connection-pool': parent,
    keys: [{ 'api-key': 'fixture' }],
  });
  const save = (pool?: ProviderKeyConfig['connectionPool']) =>
    providersApi.updateGeminiKey('fixture', undefined, { ...b.row(), connectionPool: pool });
  expect(b.row().connectionPool).toBeUndefined();
  await save();
  expect(b.key()).not.toHaveProperty('connection-pool');
  await save({ enabled: true });
  expect(b.key()['connection-pool']).toEqual({ enabled: true });
  await save({ enabled: false, maxIdleConnsPerHost: 0, idleConnTimeout: '-1s' });
  expect(b.key()['connection-pool']).toEqual({
    enabled: false,
    'idle-conn-timeout': '-1s',
    'max-idle-conns-per-host': 0,
  });
  await save({});
  expect(b.key()['connection-pool']).toEqual({});
  await save();
  expect(b.key()['connection-pool']).toBeNull();
  expect(b.group()['connection-pool']).toEqual(parent);
});

test('Vertex accepts model OpenAPI overrides without an alias and distinguishes false from inherit', async () => {
  const b = backend('vertex', {
    name: 'fixture',
    keys: [
      {
        'api-key': 'fixture',
        interactions: false,
        openapi: true,
        'openapi-location': 'global',
        models: [{ name: 'model', openapi: false }],
      },
    ],
  });
  const row = b.row();
  expect(row).toMatchObject({ interactions: false, openapi: true, openapiLocation: 'global' });
  const model = row.models![0];
  const draft = { name: model.name, ...readModelOptions(model) };
  expect(buildModelOptions(draft).openapi).toBe(false);
  await providersApi.updateVertexConfig(row.apiKey, undefined, {
    ...row,
    openapi: false,
    interactions: true,
    openapiLocation: ' us-central1 ',
    models: [{ ...model, openapi: undefined }],
  });
  expect(b.key()).toEqual({
    'api-key': 'fixture',
    interactions: true,
    openapi: false,
    'openapi-location': 'us-central1',
    models: [{ name: 'model' }],
  });
  await providersApi.updateVertexConfig(row.apiKey, undefined, {
    ...b.row(),
    interactions: undefined,
    openapi: undefined,
    openapiLocation: '',
    models: [{ ...model, openapi: false }],
  });
  expect(b.key()).toEqual({ 'api-key': 'fixture', models: [{ name: 'model', openapi: false }] });
});

test('Claude native tool search is explicit and protocol-specific', async () => {
  const b = backend('claude', {
    name: 'fixture',
    keys: [{ 'api-key': 'fixture', 'native-tool-search': true }],
  });
  expect(b.row().nativeToolSearch).toBe(true);
  await providersApi.updateClaudeConfig('fixture', undefined, {
    ...b.row(),
    nativeToolSearch: false,
    interactions: true,
    openapi: true,
    openapiLocation: 'global',
  });
  expect(b.key()).toEqual({ 'api-key': 'fixture', 'native-tool-search': false });
  await providersApi.updateClaudeConfig('fixture', undefined, {
    ...b.row(),
    nativeToolSearch: undefined,
  });
  expect(b.key()).not.toHaveProperty('native-tool-search');
});

test('pool validation accepts Go durations and non-positive limits without blocking unrelated edits', () => {
  for (const timeout of ['', '5m30s', '-2s', '0'])
    expect(
      validateConnectionPool({ idleConnTimeout: timeout, maxIdleConnsPerHost: -1 })
    ).toBeNull();
  expect(validateConnectionPool({ idleConnTimeout: '5 minutes' })).toContain('invalid_duration');
  expect(validateConnectionPool({ maxIdleConnsPerHost: 1.5 })).toContain('invalidMaxIdle');
  const legacy = { idleConnTimeout: 'custom-token' };
  expect(validateConnectionPool({ ...legacy, enabled: false }, legacy)).toBeNull();
});
