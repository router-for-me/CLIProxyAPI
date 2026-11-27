import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import type { TFunction } from 'i18next';
import { syncModelCatalog } from '../src/features/authFiles/modelCatalogSync';
import { modelsApi } from '../src/services/api/models';
import { apiClient } from '../src/services/api/client';

const read = (path: string) => readFileSync(path, 'utf8');

function harness() {
  let revision = 1;
  let mounted = true;
  let resolve!: (value: { changedProviders: string[] }) => void;
  let reject!: (error: Error) => void;
  let calls = 0;
  let invalidations = 0;
  let refetches = 0;
  let loading = false;
  const notifications: unknown[][] = [];
  const pending = { current: false };
  const t = ((key: string, params?: { providers: string }) =>
    `${key}${params?.providers ?? ''}`) as TFunction;
  const deps = {
    refreshCatalog: () => {
      calls++;
      return new Promise<{ changedProviders: string[] }>((res, rej) => {
        resolve = res;
        reject = rej;
      });
    },
    isConnected: () => true,
    getConnectionRevision: () => revision,
    isMounted: () => mounted,
    setSyncing: (value: boolean) => {
      loading = value;
    },
    clearModelCache: () => invalidations++,
    onSynced: () => refetches++,
    showNotification: (...args: unknown[]) => notifications.push(args),
    t,
  };
  return {
    sync: () => syncModelCatalog(deps, pending),
    resolve: (providers: string[]) => resolve({ changedProviders: providers }),
    reject: () => reject(new Error('fixture failure')),
    switchConnection: () => revision++,
    unmount: () => {
      mounted = false;
    },
    disconnect: () => {
      deps.isConnected = () => false;
    },
    notifications,
    get calls() {
      return calls;
    },
    get invalidations() {
      return invalidations;
    },
    get refetches() {
      return refetches;
    },
    get loading() {
      return loading;
    },
  };
}

describe('global model catalog sync', () => {
  test('posts through the management client and normalizes null providers', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ ok: true, changed_providers: null });
    try {
      expect(await modelsApi.refreshCatalog()).toEqual({ changedProviders: [] });
      expect(post).toHaveBeenCalledWith('/models/refresh');
      post.mockResolvedValue({ ok: true, changed_providers: ['claude'] });
      expect(await modelsApi.refreshCatalog()).toEqual({ changedProviders: ['claude'] });
    } finally {
      post.mockRestore();
    }
  });

  test('pins a scoped catalog update to its owning management connection', async () => {
    const client = apiClient.captureConnection();
    const post = spyOn(client, 'post').mockResolvedValue({ changed_providers: [] });
    const capture = spyOn(apiClient, 'captureConnection').mockReturnValue(client);
    const isCurrent = () => true;
    try {
      expect(await modelsApi.refreshCatalog(isCurrent)).toEqual({ changedProviders: [] });
      expect(capture).toHaveBeenCalledWith(isCurrent);
      expect(post).toHaveBeenCalledWith('/models/refresh');
    } finally {
      capture.mockRestore();
      post.mockRestore();
    }
  });

  for (const providers of [[], ['claude', 'codex']]) {
    test(`invalidates and refetches even when unchanged: ${providers}`, async () => {
      const h = harness();
      const pending = h.sync();
      await h.sync();
      expect(h.calls).toBe(1);
      expect(h.loading).toBe(true);
      h.resolve(providers);
      await pending;
      expect(h.loading).toBe(false);
      expect(h.invalidations).toBe(1);
      expect(h.refetches).toBe(1);
      expect(h.notifications[0]).toEqual([
        providers.length
          ? 'auth_files.sync_catalog_successclaude, codex'
          : 'auth_files.sync_catalog_unchanged',
        'success',
      ]);
    });
  }

  test('reports errors without clearing valid caches and allows retry', async () => {
    const h = harness();
    const pending = h.sync();
    h.reject();
    await pending;
    expect(h.notifications[0]).toEqual([
      'auth_files.sync_catalog_failed: fixture failure',
      'error',
    ]);
    expect(h.invalidations).toBe(0);
    expect(h.loading).toBe(false);
    const retry = h.sync();
    h.resolve([]);
    await retry;
    expect(h.calls).toBe(2);
  });

  for (const stale of ['switchConnection', 'unmount'] as const) {
    for (const fail of [false, true]) {
      test(`ignores stale ${fail ? 'error' : 'success'} after ${stale}`, async () => {
        const h = harness();
        const pending = h.sync();
        h[stale]();
        if (fail) h.reject();
        else h.resolve(['claude']);
        await pending;
        expect(h.notifications).toEqual([]);
        expect(h.invalidations).toBe(0);
        expect(h.refetches).toBe(0);
      });
    }
  }

  test('does nothing while disconnected', async () => {
    const h = harness();
    h.disconnect();
    await h.sync();
    expect(h.calls).toBe(0);
    expect(h.loading).toBe(false);
  });

  test('system model description names both refresh buttons in every locale', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
      const system = JSON.parse(read(`src/i18n/locales/${locale}.json`)).system_info;
      expect(system.models_desc).toContain(system.models_refresh_button);
      expect(system.models_desc).toContain(system.models_catalog_refresh_button);
    }
  });
});
