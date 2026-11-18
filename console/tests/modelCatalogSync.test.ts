import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import { modelsApi } from '../src/services/api/models';
import { apiClient } from '../src/services/api/client';

const read = (path: string) => readFileSync(path, 'utf8');
const hook = read('src/features/authFiles/hooks/useModelCatalogSync.ts');

function harness() {
  let revision = 1;
  let resolve!: (value: { changedProviders: string[] }) => void;
  let reject!: (error: Error) => void;
  let calls = 0;
  let invalidations = 0;
  let refetches = 0;
  let loading = false;
  const notifications: unknown[][] = [];
  const mountedRef = { current: true };
  const env = {
    useCallback: (fn: unknown) => fn,
    pendingRef: { current: false },
    mountedRef,
    apiClient: { getConnectionRevision: () => revision },
    useAuthStore: { getState: () => ({ connectionStatus: 'connected' }) },
    modelsApi: {
      refreshCatalog: () => {
        calls++;
        return new Promise((res, rej) => {
          resolve = res;
          reject = rej;
        });
      },
    },
    useModelsStore: { getState: () => ({ clearCache: () => invalidations++ }) },
    onSyncedRef: { current: () => refetches++ },
    setSyncingModelCatalog: (value: boolean) => {
      loading = value;
    },
    showNotification: (...args: unknown[]) => notifications.push(args),
    t: (key: string, params?: { providers: string }) => `${key}${params?.providers ?? ''}`,
  };
  const block = hook.slice(hook.indexOf('  const syncModelCatalog ='), hook.indexOf('  return {'));
  const js = ts.transpileModule(`${block}\nreturn syncModelCatalog;`, {
    compilerOptions: { target: ts.ScriptTarget.ES2022 },
  }).outputText;
  return {
    sync: new Function(...Object.keys(env), js)(...Object.values(env)) as () => Promise<void>,
    resolve: (providers: string[]) => resolve({ changedProviders: providers }),
    reject: () => reject(new Error('fixture failure')),
    switchConnection: () => revision++,
    unmount: () => {
      mountedRef.current = false;
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

  test('places secondary sync first and wires model-only cache refresh', () => {
    const header = read('src/features/authFiles/components/VaultHeader.tsx');
    const actions = header.slice(header.indexOf('<div className={styles.actions}'));
    expect(actions.indexOf('onClick={onSyncModelCatalog}')).toBeLessThan(
      actions.indexOf('onClick={onRefreshCredentials}')
    );
    expect(actions.slice(0, actions.indexOf('{onRefreshCredentials'))).toContain(
      'styles.ghostAction'
    );
    expect(actions.slice(0, actions.indexOf('{onRefreshCredentials'))).not.toContain('title=');
    const page = read('src/features/authFiles/AuthFilesPage.tsx');
    const callback = page.slice(
      page.indexOf('useModelCatalogSync(() =>'),
      page.indexOf('const invalidateDerivedCaches')
    );
    expect(callback).toContain('invalidateModels()');
    expect(callback).toContain('void showModels(');
    expect(callback).not.toContain('invalidateDerivedCaches');
    // The alias diagram left this page: the model rules workspace fetches provider catalogs
    // fresh on mount, so the page no longer keeps a catalog version to re-trigger them.
    expect(page).not.toContain('modelCatalogVersion');
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
      const translations = JSON.parse(read(`src/i18n/locales/${locale}.json`));
      const system = translations.system_info;
      expect(system.models_desc).toContain(system.models_refresh_button);
      expect(system.models_desc).toContain(system.models_catalog_refresh_button);
      const messages = translations.auth_files;
      expect(messages).not.toHaveProperty('sync_catalog_tooltip');
      for (const key of ['button', 'success', 'unchanged', 'failed']) {
        expect(messages[`sync_catalog_${key}`]).toBeTruthy();
      }
      expect(messages.sync_catalog_success).toContain('{{providers}}');
    }
  });
});
