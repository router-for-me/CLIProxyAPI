import { describe, expect, test } from 'bun:test';
import type { TFunction } from 'i18next';
import {
  createAuthFileRefreshPending,
  refreshAllAuthFiles,
  refreshAuthFile,
  type AuthFileRefreshDeps,
} from '@/features/authFiles/refreshActions';
import type { AuthFileItem } from '@/types';

function harness() {
  let revision = 1;
  let confirmation: (() => Promise<void>) | undefined;
  let results: unknown = null;
  let loads = 0;
  let notifications = 0;
  let invalidations = 0;
  let mutations = 0;
  let refreshing: Record<string, boolean> = {};
  const calls: unknown[][] = [];
  let resolve!: (value: unknown) => void;
  let reject!: (error: Error) => void;
  const request = <T>(...args: unknown[]) => {
    calls.push(args);
    return new Promise<T>((res, rej) => {
      resolve = res as (value: unknown) => void;
      reject = rej;
    });
  };
  const pending = createAuthFileRefreshPending();
  const deps: AuthFileRefreshDeps = {
    requestManualRefresh: (...args) => request(...args),
    requestAllManualRefresh: () => request(),
    getConnectionRevision: () => revision,
    setManualRefreshing: (update) => {
      refreshing = typeof update === 'function' ? update(refreshing) : update;
    },
    setRefreshingAllCredentials: () => {},
    setRefreshResults: (value) => {
      results = value;
    },
    showConfirmation: (options) => {
      confirmation = options.onConfirm;
    },
    showNotification: () => {
      notifications++;
    },
    invalidateInFlightLoads: () => {
      invalidations++;
    },
    notifyAuthFilesChanged: () => {},
    onFilesMutated: () => {
      mutations++;
    },
    reloadFiles: async () => {
      loads++;
    },
    t: ((key: string) => key) as TFunction,
  };
  return {
    single: (item: AuthFileItem) => refreshAuthFile(deps, pending, item),
    all: () => refreshAllAuthFiles(deps, pending),
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
    get mutations() {
      return mutations;
    },
    get refreshing() {
      return refreshing;
    },
  };
}

const credential = (authIndex: string): AuthFileItem => ({
  name: 'one.json',
  type: 'codex',
  authIndex,
});

describe('credential refresh action lifecycle', () => {
  test('requires confirmation, prevents overlap, and exposes individual results', async () => {
    const h = harness();
    h.all();
    expect(h.calls).toHaveLength(0);
    const pending = h.confirm();
    await h.confirm();
    await h.single(credential('one'));
    expect(h.calls).toHaveLength(1);
    const results = [{ id: 'one.json', success: false, error: 'fixture' }];
    h.resolve(results);
    await pending;
    expect(h.results).toEqual(results);
    expect(h.loads).toBe(1);
    expect(h.invalidations).toBe(1);
    expect(h.mutations).toBe(1);
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
      const pending = all ? h.confirm() : h.single(credential('one'));
      h.switchConnection();
      h.resolve([{ id: 'one.json', success: true }]);
      await pending;
      expect(h.results).toBeNull();
      expect(h.notifications).toBe(0);
      expect(h.loads).toBe(0);
      expect(h.invalidations).toBe(0);
      expect(h.mutations).toBe(0);
    });
  }

  test('single refresh forwards the selected identity and blocks a duplicate', async () => {
    const h = harness();
    const item = credential('two');
    const pending = h.single(item);
    await h.single(item);
    h.all();
    expect(h.confirm()).toBeUndefined();
    expect(h.calls).toEqual([['one.json', 'two']]);
    h.resolve(undefined);
    await pending;
    expect(h.loads).toBe(1);
    expect(h.mutations).toBe(1);
  });

  test('a failed single refresh reports the error without reloading', async () => {
    const h = harness();
    const pending = h.single(credential('one'));
    expect(Object.values(h.refreshing)).toEqual([true]);
    h.reject(new Error('expired'));
    await pending;
    expect(h.notifications).toBe(1);
    expect(h.loads).toBe(0);
    expect(h.refreshing).toEqual({});
    // The identity is released, so a retry is sent.
    void h.single(credential('one'));
    expect(h.calls).toHaveLength(2);
  });

  test.each([
    ['disabled', { ...credential('x'), disabled: true }],
    ['unsupported provider', { ...credential('x'), type: 'gemini-cli' }],
    ['blank name', { ...credential('x'), name: '  ' }],
  ])('ignores a %s credential', async (_, item) => {
    const h = harness();
    await h.single(item);
    expect(h.calls).toEqual([]);
  });
});
