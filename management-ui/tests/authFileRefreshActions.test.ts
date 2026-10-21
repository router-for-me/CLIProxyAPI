import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import { getAuthFileRefreshKey } from '../src/features/authFiles/manualRefresh';

const source = readFileSync('src/features/authFiles/hooks/useAuthFilesData.ts', 'utf8');
function harness() {
  let revision = 1;
  let confirmation: (() => Promise<void>) | undefined;
  let results: unknown = null;
  let loads = 0;
  let notifications = 0;
  let invalidations = 0;
  const calls: unknown[][] = [];
  let resolve!: (value: unknown) => void;
  let reject!: (error: Error) => void;
  const request = (...args: unknown[]) => {
    calls.push(args);
    return new Promise((res, rej) => {
      resolve = res;
      reject = rej;
    });
  };
  const env = {
    apiClient: { getConnectionRevision: () => revision },
    authFilesApi: { requestManualRefresh: request, requestAllManualRefresh: request },
    getAuthFileRefreshKey,
    isRuntimeOnlyAuthFile: () => false,
    supportsAuthFileManualRefresh: () => true,
    refreshAllPendingRef: { current: false },
    manualRefreshPendingRef: { current: new Set<string>() },
    setManualRefreshing: () => {},
    setRefreshingAllCredentials: () => {},
    setRefreshResults: (value: unknown) => {
      results = value;
    },
    showConfirmation: (options: { onConfirm: () => Promise<void> }) => {
      confirmation = options.onConfirm;
    },
    showNotification: () => {
      notifications++;
    },
    invalidateInFlightLoads: () => {
      invalidations++;
    },
    notifyAuthFilesChanged: () => {},
    onFilesMutatedRef: { current: () => {} },
    loadFiles: async () => {
      loads++;
    },
    t: (key: string) => key,
    useCallback: (fn: unknown) => fn,
  };
  const block = source.slice(
    source.indexOf('  const handleManualRefresh = useCallback('),
    source.indexOf('  const handleCooldownReset = useCallback(')
  );
  const js = ts.transpileModule(
    `${block}\nreturn {single: handleManualRefresh, all: handleRefreshAllCredentials};`,
    {
      compilerOptions: { target: ts.ScriptTarget.ES2022 },
    }
  ).outputText;
  const callbacks = new Function(...Object.keys(env), js)(...Object.values(env));
  return {
    ...callbacks,
    confirm: () => confirmation?.(),
    switchConnection: () => {
      revision++;
    },
    resolve: (value: unknown) => resolve(value),
    reject: (error: Error) => reject(error),
    calls,
    get results() {
      return results;
    },
    get loads() {
      return loads;
    },
    get notifications() {
      return notifications;
    },
    get invalidations() {
      return invalidations;
    },
  };
}

describe('credential refresh action lifecycle', () => {
  test('requires confirmation, prevents overlap, and exposes individual results', async () => {
    const h = harness();
    h.all();
    expect(h.calls).toHaveLength(0);
    const pending = h.confirm();
    await h.confirm();
    await h.single({ name: 'one.json', authIndex: 'one' });
    expect(h.calls).toHaveLength(1);
    const results = [{ id: 'one.json', success: false, error: 'fixture' }];
    h.resolve(results);
    await pending;
    expect(h.results).toEqual(results);
    expect(h.loads).toBe(1);
    expect(h.invalidations).toBe(1);
  });

  test('a failed all-refresh still reloads potentially changed backend state', async () => {
    const h = harness();
    h.all();
    const pending = h.confirm();
    h.reject(new Error('timeout'));
    await pending;
    expect(h.results).toBeNull();
    expect(h.notifications).toBe(1);
    expect(h.loads).toBe(1);
  });

  test('a confirmation from an old connection cannot send a request', async () => {
    const h = harness();
    h.all();
    h.switchConnection();
    await h.confirm();
    expect(h.calls).toHaveLength(0);
  });

  for (const all of [true, false]) {
    test(`stale ${all ? 'all' : 'single'} refresh cannot publish results or invalidate new caches`, async () => {
      const h = harness();
      if (all) h.all();
      const pending = all ? h.confirm() : h.single({ name: 'one.json', authIndex: 'one' });
      h.switchConnection();
      h.resolve([{ id: 'one.json', success: true }]);
      await pending;
      expect(h.results).toBeNull();
      expect(h.notifications).toBe(0);
      expect(h.loads).toBe(0);
      expect(h.invalidations).toBe(0);
    });
  }

  test('single refresh forwards the selected identity and blocks a duplicate', async () => {
    const h = harness();
    const item = { name: 'one.json', authIndex: 'two' };
    const pending = h.single(item);
    await h.single(item);
    h.all();
    expect(h.confirm()).toBeUndefined();
    expect(h.calls).toEqual([['one.json', 'two']]);
    h.resolve(undefined);
    await pending;
    expect(h.loads).toBe(1);
  });
});
