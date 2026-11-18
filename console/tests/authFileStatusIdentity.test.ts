import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import ts from 'typescript';
import { getAuthFileRefreshKey } from '../src/features/authFiles/manualRefresh';

const source = readFileSync('src/features/authFiles/hooks/useAuthFilesData.ts', 'utf8');

// Execute the actual callback bodies without mocking React globally or requiring a DOM.
function harness() {
  const original = [
    { name: 'shared.json', authIndex: 'a', disabled: false },
    { name: 'shared.json', authIndex: 'b', disabled: true },
  ];
  let files = original.map((file) => ({ ...file }));
  let revision = 1;
  let notifications = 0;
  let deselections = 0;
  const requests: {
    args: unknown[];
    resolve: (v: { disabled: boolean }) => void;
    reject: (e: Error) => void;
  }[] = [];
  const env = {
    files: original,
    getAuthFileRefreshKey,
    apiClient: { getConnectionRevision: () => revision },
    authFilesApi: {
      setStatus: (...args: unknown[]) =>
        new Promise<{ disabled: boolean }>((resolve, reject) =>
          requests.push({ args, resolve, reject })
        ),
    },
    statusPendingRef: { current: { revision: -1, keys: new Set<string>() } },
    batchStatusPendingRef: { current: false },
    setStatusUpdating: () => {},
    setBatchStatusUpdating: () => {},
    setFiles: (update: (prev: typeof files) => typeof files) => {
      files = update(files);
    },
    invalidateInFlightLoads: () => {},
    showNotification: () => {
      notifications++;
    },
    deselectAll: () => {
      deselections++;
    },
    t: (key: string) => key,
    useCallback: (fn: unknown) => fn,
  };
  function callback(name: string, next: string) {
    const block = source.slice(
      source.indexOf(`  const ${name} = useCallback(`),
      source.indexOf(`  const ${next} = useCallback(`)
    );
    const js = ts.transpileModule(`${block}\nreturn ${name};`, {
      compilerOptions: { target: ts.ScriptTarget.ES2022 },
    }).outputText;
    return new Function(...Object.keys(env), js)(...Object.values(env));
  }
  return {
    toggle: callback('handleStatusToggle', 'batchSetStatus'),
    batch: callback('batchSetStatus', 'batchDownload'),
    original,
    requests,
    get files() {
      return files;
    },
    get notifications() {
      return notifications;
    },
    get deselections() {
      return deselections;
    },
    switchConnection() {
      revision++;
      files = original.map((file) => ({ ...file }));
    },
  };
}

describe('auth file status identity', () => {
  test('status API sends optional auth_index without changing name-only requests', () => {
    const apiSource = readFileSync('src/services/api/authFiles.ts', 'utf8');
    const expression = apiSource.slice(
      apiSource.indexOf('  setStatus:'),
      apiSource.indexOf('  patchFields:')
    );
    const js = ts.transpileModule(`return ({ ${expression} });`, {
      compilerOptions: { target: ts.ScriptTarget.ES2022 },
    }).outputText;
    const calls: unknown[][] = [];
    const api = new Function('apiClient', js)({ patch: (...args: unknown[]) => calls.push(args) });
    api.setStatus('shared.json', true, 'b');
    api.setStatus('ordinary.json', false);
    expect(calls).toEqual([
      ['/credentials/status', { name: 'shared.json', disabled: true, auth_index: 'b' }],
      ['/credentials/status', { name: 'ordinary.json', disabled: false }],
    ]);
  });
  test('single toggle isolates same-name identities and suppresses duplicate submissions', async () => {
    const h = harness();
    const pending = h.toggle(h.original[0], false);
    await h.toggle(h.original[0], false);
    expect(h.requests.map((r) => r.args)).toEqual([['shared.json', true, 'a']]);
    h.requests[0].reject(new Error('failed'));
    await pending;
    expect(h.files.map((f) => f.disabled)).toEqual([false, true]);
  });

  test('batch processes every selected identity and independently rolls back failures', async () => {
    const h = harness();
    const pending = h.batch(['shared.json', 'shared.json'], true);
    await h.toggle(h.original[1], false);
    await h.batch(['shared.json'], true);
    expect(h.requests.map((r) => r.args)).toEqual([
      ['shared.json', false, 'a'],
      ['shared.json', false, 'b'],
    ]);
    h.requests[0].resolve({ disabled: false });
    h.requests[1].reject(new Error('failed'));
    await pending;
    expect(h.files.map((f) => f.disabled)).toEqual([false, true]);
    expect(h.deselections).toBe(1);
  });

  test('batch cannot overlap an in-flight single identity', async () => {
    const h = harness();
    const pending = h.toggle(h.original[0], false);
    await h.batch(['shared.json'], true);
    expect(h.requests).toHaveLength(1);
    h.requests[0].resolve({ disabled: true });
    await pending;
  });

  for (const batch of [false, true]) {
    test(`stale ${batch ? 'batch' : 'single'} completion cannot mutate a new connection`, async () => {
      const h = harness();
      const pending = batch ? h.batch(['shared.json'], false) : h.toggle(h.original[0], false);
      h.switchConnection();
      const fresh = h.toggle(h.original[0], false);
      h.requests[0].reject(new Error('old connection'));
      if (batch) h.requests[1].resolve({ disabled: true });
      await pending;
      expect(h.notifications).toBe(0);
      expect(h.deselections).toBe(0);
      await h.toggle(h.original[0], false);
      expect(h.requests).toHaveLength(batch ? 3 : 2);
      h.requests.at(-1)!.resolve({ disabled: false });
      await fresh;
      expect(h.files.map((f) => f.disabled)).toEqual([false, true]);
    });
  }
});
