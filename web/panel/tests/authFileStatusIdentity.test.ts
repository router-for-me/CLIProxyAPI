import { describe, expect, spyOn, test } from 'bun:test';
import type { TFunction } from 'i18next';
import { authFilesApi } from '@/services/api/authFiles';
import { apiClient } from '@/services/api/client';
import {
  batchSetAuthFileStatus,
  createAuthFileStatusPending,
  toggleAuthFileStatus,
  type AuthFileStatusDeps,
} from '@/features/authFiles/statusActions';
import type { AuthFileItem } from '@/types';

type Request = {
  args: unknown[];
  resolve: (value: { disabled: boolean }) => void;
  reject: (error: Error) => void;
};

function harness() {
  const original: AuthFileItem[] = [
    { name: 'shared.json', authIndex: 'a', disabled: false },
    { name: 'shared.json', authIndex: 'b', disabled: true },
    { name: 'other.json', authIndex: 'c', disabled: false },
  ];
  let files = original.map((file) => ({ ...file }));
  let revision = 1;
  let notifications = 0;
  let deselections = 0;
  let updating: Record<string, boolean> = {};
  const requests: Request[] = [];
  const pending = createAuthFileStatusPending();
  const deps: AuthFileStatusDeps & { deselectAll: () => void } = {
    setStatus: (...args) =>
      new Promise((resolve, reject) => requests.push({ args, resolve, reject })),
    getConnectionRevision: () => revision,
    setFiles: (update) => {
      files = typeof update === 'function' ? update(files) : update;
    },
    setStatusUpdating: (update) => {
      updating = typeof update === 'function' ? update(updating) : update;
    },
    setBatchStatusUpdating: () => {},
    invalidateInFlightLoads: () => {},
    showNotification: () => {
      notifications++;
    },
    t: ((key: string) => key) as TFunction,
    deselectAll: () => {
      deselections++;
    },
  };
  return {
    toggle: (item: AuthFileItem, enabled: boolean) =>
      toggleAuthFileStatus(deps, pending, item, enabled),
    batch: (names: string[], enabled: boolean) =>
      batchSetAuthFileStatus(deps, pending, original, names, enabled),
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
    get updating() {
      return updating;
    },
    switchConnection() {
      revision++;
      files = original.map((file) => ({ ...file }));
    },
  };
}

describe('auth file status identity', () => {
  test('status API sends optional auth_index without changing name-only requests', async () => {
    const patch = spyOn(apiClient, 'patch').mockResolvedValue({ disabled: true });
    try {
      await authFilesApi.setStatus('shared.json', true, 'b');
      await authFilesApi.setStatus('ordinary.json', false);
      expect(patch.mock.calls).toEqual([
        ['/credentials/status', { name: 'shared.json', disabled: true, auth_index: 'b' }],
        ['/credentials/status', { name: 'ordinary.json', disabled: false }],
      ]);
    } finally {
      patch.mockRestore();
    }
  });

  test('single toggle isolates same-name identities and suppresses duplicate submissions', async () => {
    const h = harness();
    const pending = h.toggle(h.original[0], false);
    await h.toggle(h.original[0], false);
    expect(h.requests.map((r) => r.args)).toEqual([['shared.json', true, 'a']]);
    expect(h.files.map((f) => f.disabled)).toEqual([true, true, false]);
    expect(Object.values(h.updating)).toEqual([true]);
    h.requests[0].reject(new Error('failed'));
    await pending;
    expect(h.updating).toEqual({});
    expect(h.notifications).toBe(1);
    expect(h.files.map((f) => f.disabled)).toEqual([false, true, false]);
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
    expect(Object.keys(h.updating)).toHaveLength(2);
    h.requests[1].reject(new Error('failed'));
    await pending;
    expect(h.files.map((f) => f.disabled)).toEqual([false, true, false]);
    expect(h.deselections).toBe(1);
    expect(h.updating).toEqual({});
  });

  test('only one batch runs at a time, even for unrelated credentials', async () => {
    const h = harness();
    const pending = h.batch(['shared.json'], true);
    await h.batch(['other.json'], false);
    expect(h.requests.map((r) => r.args[0])).toEqual(['shared.json', 'shared.json']);
    h.requests.forEach((r) => r.resolve({ disabled: false }));
    await pending;
    const next = h.batch(['other.json'], false);
    expect(h.requests.at(-1)?.args).toEqual(['other.json', true, 'c']);
    h.requests.at(-1)!.resolve({ disabled: true });
    await next;
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
    for (const outcome of ['failure', 'success'] as const) {
      test(`stale ${batch ? 'batch' : 'single'} ${outcome} cannot mutate a new connection`, async () => {
        const h = harness();
        const pending = batch ? h.batch(['shared.json'], false) : h.toggle(h.original[0], false);
        h.switchConnection();
        const fresh = h.toggle(h.original[0], false);
        const stale = h.requests.slice(0, batch ? 2 : 1);
        for (const request of stale) {
          if (outcome === 'success') request.resolve({ disabled: true });
          else request.reject(new Error('old connection'));
        }
        await pending;
        expect(h.notifications).toBe(0);
        expect(h.deselections).toBe(0);
        await h.toggle(h.original[0], false);
        expect(h.requests).toHaveLength(stale.length + 1);
        h.requests.at(-1)!.resolve({ disabled: false });
        await fresh;
        expect(h.files.map((f) => f.disabled)).toEqual([false, true, false]);
      });
    }
  }
});
