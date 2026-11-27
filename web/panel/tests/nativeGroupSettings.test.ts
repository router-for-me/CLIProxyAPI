import { describe, expect, test } from 'bun:test';
import {
  readNativeGroupSettings as read,
  buildNativeGroupSettings as build,
} from '@/features/providers/nativeGroupSettings';

const group = {
  name: 'alpha',
  'base-url': 'https://example.test',
  headers: {},
  prefix: null,
  'disable-cooling': false,
  'request-retry': 0,
  'request-scoped-errors': [],
  'connection-pool': { enabled: false, 'idle-conn-timeout': '300s', 'max-idle-conns-per-host': 0 },
  'excluded-models': ['*'],
  models: [
    { name: 'm', alias: 'first' },
    { name: 'm', alias: 'second', openapi: false },
  ],
  keys: [
    { 'api-key': 'fixture-private-member', priority: 1, 'connection-pool': { enabled: true } },
  ],
};
describe('native group settings', () => {
  test('renaming leaves all other group and member fields intact', () => {
    const result = build(group, { ...read(group), name: 'renamed' });
    expect(result).toEqual({ ...group, name: 'renamed' });
    expect(result.keys).toBe(group.keys);
    expect(group.name).toBe('alpha');
  });
  test('absent pool, empty opt-in, explicit opt-out and signed options remain distinct', () => {
    const start = { keys: [] };
    const draft = read(start);
    expect(build(start, draft)).toEqual(start);
    const opted = build(start, { ...draft, poolPresent: true });
    expect(opted).toEqual({ ...start, 'connection-pool': {} });
    const configured = build(opted, {
      ...read(opted),
      poolEnabled: 'false',
      poolTimeout: '-1m2s',
      poolLimit: '-1',
    });
    expect(configured['connection-pool']).toEqual({
      enabled: false,
      'idle-conn-timeout': '-1m2s',
      'max-idle-conns-per-host': -1,
    });
    expect(build(configured, { ...read(configured), poolPresent: false })).toEqual(start);
    expect(
      build(group, { ...read(group), poolEnabled: '', poolLimit: '' })['connection-pool']
    ).toEqual({ 'idle-conn-timeout': '300s' });
  });
  test('validates signed integer, Go duration, and string header map', () => {
    for (const patch of [
      { priority: '1.2' },
      { poolLimit: '999999999999999999' },
      { poolTimeout: '1day' },
      { headers: '[]' },
      { headers: '{' },
      { headers: '{"X":3}' },
    ]) {
      expect(() => build(group, { ...read(group), ...patch })).toThrow();
    }
  });
  test('group-only base URL, defaults and models never mutate members', () => {
    const draft = read(group);
    const update = build(
      group,
      {
        ...draft,
        baseUrl: 'https://other.example.test',
        proxyUrl: 'direct',
        prefix: 'team',
        priority: '-3',
        headers: '{"X-Test":"a"}',
        excludedModels: 'model-*\nother',
        models: [{ ...draft.models[1], alias: 'edited' }],
      },
      'vertex'
    );
    expect(update).toMatchObject({
      'base-url': 'https://other.example.test',
      'proxy-url': 'direct',
      prefix: 'team',
      priority: -3,
      headers: { 'X-Test': 'a' },
      'excluded-models': ['model-*', 'other'],
      models: [{ name: 'm', alias: 'edited', openapi: false }],
    });
    expect(update.keys).toBe(group.keys);
  });
  test('runtime policy omits inherited fields and preserves explicit zero, false, empty rules and wire match-regexr', () => {
    const rules = [{ status: 400, action: 'stop', 'match-regexr': ['^invalid'] }];
    const start = { keys: [], 'request-scoped-errors': rules };
    const draft = read(start);
    draft.policy.retry = '0';
    draft.policy.cooling = 'enabled';
    draft.policy.errorRules[0].matches[0].value = '^new';
    const next = build(start, draft);
    expect(next).toMatchObject({
      'request-retry': 0,
      'disable-cooling': false,
      'request-scoped-errors': [{ ...rules[0], 'match-regexr': ['^new'] }],
    });
    const empty = read(next);
    empty.policy.errorRules = [];
    expect(build(next, empty)['request-scoped-errors']).toEqual([]);
    empty.policy.errorsMode = 'inherit';
    empty.policy.retry = '';
    empty.policy.cooling = 'inherit';
    expect(build(next, empty)).toEqual({ keys: [] });
    expect(build(start, draft, 'vertex')['request-scoped-errors']).toBe(rules);
  });
});
