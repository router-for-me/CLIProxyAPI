import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { parseDocument } from 'yaml';
import {
  applyConfigPatch,
  buildConfigPatch,
  hasConfigPatchChanges,
} from '../src/services/api/configPatch';
import { apiClient } from '../src/services/api/client';
import { runVisualConfig } from './helpers/visualConfig';

describe('buildConfigPatch', () => {
  for (const [field, path] of [
    ['port', 'server.port'],
    ['requestRetry', 'routing.retry.request-retry'],
    ['maxRetryCredentials', 'routing.retry.max-retry-credentials'],
    ['maxRetryInterval', 'routing.retry.max-retry-interval'],
    ['authAutoRefreshWorkers', 'oauth.auth-auto-refresh-workers'],
    ['errorLogsMaxFiles', 'observability.logs.error-logs-max-files'],
    ['logsMaxTotalSizeMb', 'observability.logs.logs-max-total-size-mb'],
    ['redisUsageQueueRetentionSeconds', 'observability.usage.redis-usage-queue-retention-seconds'],
  ] as const) {
    test(`visual numeric clearing deletes only ${path}`, () => {
      const doc = parseDocument('unknown: {secret: unchanged}\n');
      doc.setIn(path.split('.'), 3);
      const before = doc.toString();
      const visual = runVisualConfig(before, [{ [field]: '' }]);
      expect(buildConfigPatch(before, visual.applyVisualChangesToYaml(before))).toEqual({
        patch: {},
        deletions: [path.split('.')],
      });
    });
  }

  test('real visual edits never upload untouched unknown configuration', () => {
    const before = 'plugins: {configs: {example: {secret: unchanged}}}\nserver: {port: 8317}\n';
    const visual = runVisualConfig(before, [{ port: '9000', debug: true }]);
    expect(buildConfigPatch(before, visual.applyVisualChangesToYaml(before))).toEqual({
      patch: { server: { port: 9000 }, observability: { logs: { debug: true } } },
      deletions: [],
    });
  });

  test('clearing visual Payload rules deletes only that rule list, not the payload section', () => {
    const before = `requests:
  payload:
    default:
      - models: [{name: example}]
        params: {temperature: 1}
    override:
      - models: [{name: example}]
        params: {stream: true}
    future-setting: keep
`;
    const visual = runVisualConfig(before, [{ payloadDefaultRules: [] }]);
    expect(buildConfigPatch(before, visual.applyVisualChangesToYaml(before))).toEqual({
      patch: {},
      deletions: [['requests', 'payload', 'default']],
    });
  });

  test('editing client keys replaces only the key list, not the access section', () => {
    const before = 'access: {api-keys: [fixture-old], future-setting: keep}\n';
    const visual = runVisualConfig(before, [{ apiKeysText: 'fixture-new\nfixture-second' }]);
    expect(buildConfigPatch(before, visual.applyVisualChangesToYaml(before))).toEqual({
      patch: { access: { 'api-keys': ['fixture-new', 'fixture-second'] } },
      deletions: [],
    });
  });

  test('nested edits keep siblings out of payload, and pruning deletes original leaves', () => {
    expect(
      buildConfigPatch(
        'a: {x: 1, keep: 2}\nb: {deep: {leaf: 3}, empty: {}, list: [1, 2]}',
        'a: {x: 4, keep: 2}'
      )
    ).toEqual({
      patch: { a: { x: 4 } },
      deletions: [
        ['b', 'deep', 'leaf'],
        ['b', 'empty'],
        ['b', 'list'],
      ],
    });
  });

  test('arrays replace whole, including object elements and empty arrays', () => {
    expect(buildConfigPatch('a: [{x: 1, y: 2}]\nb: [1]', 'a: [{x: 1}]\nb: []')).toEqual({
      patch: { a: [{ x: 1 }], b: [] },
      deletions: [],
    });
  });

  test('null is a value, never deletion; false, zero and empty strings survive', () => {
    expect(
      buildConfigPatch('a: 1\nb: true\nc: 2\nd: text\ne: null', 'a: null\nb: false\nc: 0\nd: ""')
    ).toEqual({
      patch: { a: null, b: false, c: 0, d: '' },
      deletions: [['e']],
    });
  });

  test('empty maps and type replacements follow recursive PATCH semantics', () => {
    expect(
      buildConfigPatch('a: {x: 1}\nb: null\nc: {}\nd: {x: 1}', 'a: {}\nb: {}\ne: {}\nd: null')
    ).toEqual({
      patch: { b: {}, e: {}, d: null },
      deletions: [['c']],
      emptyMaps: [['a']],
    });
    expect(buildConfigPatch('a: [1]', 'a: {x: 1}').patch).toEqual({ a: { x: 1 } });
  });

  test('semantic equality ignores comments, key order, scalar spelling and aliases', () => {
    const plan = buildConfigPatch(
      'a: &a {x: 1, y: true}\nb: *a',
      '# comment\nb: {y: true, x: 1.0}\na: {y: true, x: 1}'
    );
    expect(plan).toEqual({ patch: {}, deletions: [] });
    expect(hasConfigPatchChanges(plan)).toBe(false);
    expect(hasConfigPatchChanges(buildConfigPatch('a: 1', '{}'))).toBe(true);
    expect(hasConfigPatchChanges(buildConfigPatch('{}', 'a: 0'))).toBe(true);
  });

  test('prototype-like keys remain own data properties', () => {
    const plan = buildConfigPatch(
      '__proto__: {constructor: 1}\ntoString: 1',
      '__proto__: {constructor: 2}\ntoString: 0'
    );
    expect(JSON.parse(JSON.stringify(plan.patch))).toEqual(
      JSON.parse('{"__proto__":{"constructor":2},"toString":0}')
    );
    expect(Object.getPrototypeOf(plan.patch)).toBeNull();
    expect(buildConfigPatch('__proto__: {constructor: 1}', '{}').deletions).toEqual([
      ['__proto__', 'constructor'],
    ]);
  });

  for (const invalid of [
    'a: [',
    'a: 1\na: 2',
    '',
    'null',
    '[]',
    '42',
    'a: .nan',
    'a: .inf',
    'a: 9007199254740993',
    '? [a, b]\n: 1',
    '1: value',
    'a: &a [*a]',
    'a: !unknown value',
  ]) {
    test(`rejects invalid or non-JSON YAML: ${invalid}`, () => {
      expect(() => buildConfigPatch('{}', invalid)).toThrow();
      expect(() => buildConfigPatch(invalid, '{}')).toThrow();
    });
  }

  for (const key of ['', '.', '..', 'a/b', 'a\\b']) {
    test(`rejects unrepresentable deletion path ${JSON.stringify(key)}`, () => {
      const yaml = `${JSON.stringify(key)}: 1`;
      expect(() => buildConfigPatch(yaml, '{}')).toThrow();
      expect(hasConfigPatchChanges(buildConfigPatch(yaml, yaml))).toBe(false);
    });
  }
});

describe('applyConfigPatch', () => {
  const originals = {
    patch: apiClient.patch,
    delete: apiClient.delete,
    put: apiClient.put,
    revision: apiClient.getConnectionRevision,
  };
  afterEach(() => {
    apiClient.patch = originals.patch;
    apiClient.delete = originals.delete;
    apiClient.put = originals.put;
    apiClient.getConnectionRevision = originals.revision;
  });

  function setup() {
    const calls: unknown[][] = [];
    const revision = spyOn(apiClient, 'getConnectionRevision').mockReturnValue(7);
    const patch = spyOn(apiClient, 'patch').mockImplementation(async (...args) => {
      calls.push(['PATCH', ...args]);
    });
    const remove = spyOn(apiClient, 'delete').mockImplementation(async (...args) => {
      calls.push(['DELETE', ...args]);
    });
    const put = spyOn(apiClient, 'put').mockImplementation(async (...args) => {
      calls.push(['PUT', ...args]);
    });
    return { calls, revision, patch, remove, put };
  }

  test('PATCH first, then sequential encoded DELETE requests; no PUT or full upload', async () => {
    const { calls, revision } = setup();
    const plan = buildConfigPatch(
      'keep: secret\nchange: 1\n"#?% 空": {__proto__: 1}\nother: 2',
      'keep: secret\nchange: 2'
    );
    await applyConfigPatch(plan, 7);
    expect(calls).toEqual([
      ['PATCH', '/config', { change: 2 }],
      ['DELETE', `/config/${encodeURIComponent('#?% 空')}/__proto__`],
      ['DELETE', '/config/other'],
    ]);
    expect(revision).toHaveBeenCalledTimes(6);
  });

  test('no-op performs no requests; deletion-only skips PATCH', async () => {
    const { calls } = setup();
    await applyConfigPatch(buildConfigPatch('{}', '{}'), 7);
    expect(calls).toEqual([]);
    await applyConfigPatch(buildConfigPatch('a: 1', '{}'), 7);
    expect(calls).toEqual([['DELETE', '/config/a']]);
  });

  test('empty maps use targeted PUT rather than pruning their ancestors', async () => {
    const { calls } = setup();
    const plan = buildConfigPatch(
      'plugins: {configs: {demo: {options: {x: 1}, keep: true}}}',
      'plugins: {configs: {demo: {options: {}, keep: true}}}'
    );
    expect(hasConfigPatchChanges(plan)).toBe(true);
    await applyConfigPatch(plan, 7);
    expect(calls).toEqual([['PUT', '/config/plugins/configs/demo/options', {}]]);
  });

  test('targeted empty map writes precede deletions and stop on connection changes', async () => {
    const { calls, put, revision } = setup();
    const plan = buildConfigPatch('a: {x: 1}\nb: 2\nc: 1', 'a: {}\nc: 2');
    put.mockImplementationOnce(async (...args) => {
      calls.push(['PUT', ...args]);
      revision.mockReturnValue(8);
    });
    await expect(applyConfigPatch(plan, 7)).rejects.toThrow('Connection changed');
    expect(calls).toEqual([
      ['PATCH', '/config', { c: 2 }],
      ['PUT', '/config/a', {}],
    ]);
  });

  test('empty map paths are validated before any mutation', async () => {
    const { calls } = setup();
    await expect(
      applyConfigPatch({ patch: { a: 1 }, deletions: [], emptyMaps: [['..']] }, 7)
    ).rejects.toThrow();
    expect(calls).toEqual([]);
  });

  test('404 delete is idempotent and continues', async () => {
    const { remove } = setup();
    remove.mockRejectedValueOnce(Object.assign(new Error('missing'), { status: 404 }));
    await applyConfigPatch(buildConfigPatch('a: 1\nb: 2', '{}'), 7);
    expect(remove).toHaveBeenCalledTimes(2);
  });

  test('stale connection prevents first request', async () => {
    const { calls } = setup();
    await expect(applyConfigPatch(buildConfigPatch('{}', 'a: 1'), 6)).rejects.toThrow(
      'Connection changed'
    );
    expect(calls).toEqual([]);
  });

  for (const phase of ['patch', 'delete', '404'] as const) {
    test(`connection change during ${phase} stops remaining requests`, async () => {
      const { patch, remove, revision } = setup();
      const changeConnection = async () => {
        revision.mockReturnValue(8);
        if (phase === '404') throw { status: 404 };
      };
      if (phase === 'patch') patch.mockImplementationOnce(changeConnection);
      else remove.mockImplementationOnce(changeConnection);
      await expect(
        applyConfigPatch(buildConfigPatch('a: 1\nb: 2\nc: 1', 'c: 2'), 7)
      ).rejects.toThrow('Connection changed');
      expect(patch).toHaveBeenCalledTimes(1);
      expect(remove).toHaveBeenCalledTimes(phase === 'patch' ? 0 : 1);
    });
  }

  test('PATCH failure stops deletes without rollback', async () => {
    const { patch, remove } = setup();
    const failure = new Error('patch failed');
    patch.mockRejectedValueOnce(failure);
    await expect(applyConfigPatch(buildConfigPatch('a: 1\nb: 1', 'a: 2'), 7)).rejects.toBe(failure);
    expect(patch).toHaveBeenCalledTimes(1);
    expect(remove).not.toHaveBeenCalled();
  });

  test('partial delete failure propagates without rollback or subsequent deletes', async () => {
    const { patch, remove } = setup();
    const failure = Object.assign(new Error('delete failed'), { status: 500 });
    remove.mockResolvedValueOnce(undefined).mockRejectedValueOnce(failure);
    await expect(
      applyConfigPatch(buildConfigPatch('a: 1\nb: 2\nc: 3\nd: 1', 'd: 2'), 7)
    ).rejects.toBe(failure);
    expect(patch).toHaveBeenCalledTimes(1);
    expect(remove).toHaveBeenCalledTimes(2);
  });

  test('preflights all deletion paths before PATCH', async () => {
    const { calls } = setup();
    await expect(
      applyConfigPatch({ patch: { a: 1 }, deletions: [['safe'], ['..']] }, 7)
    ).rejects.toThrow();
    expect(calls).toEqual([]);
  });
});
