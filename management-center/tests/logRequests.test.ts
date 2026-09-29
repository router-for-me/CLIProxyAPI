import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createLogRequestGuard, createLogRequestQueue } from '../src/pages/hooks/logRequests';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function createReader() {
  const queue = createLogRequestQueue();
  const state = { lines: [] as string[], cursor: '', error: '', loading: false, reloads: 0 };
  const read = async (response: Promise<{ lines: string[]; cursor: string }>) => {
    const request = queue.startRead(false);
    if (request === null) return;
    state.loading = true;
    try {
      const data = await response;
      if (!queue.isCurrent(request)) return;
      state.lines = data.lines;
      state.cursor = data.cursor;
    } catch (error) {
      if (!queue.isCurrent(request)) return;
      state.error = (error as Error).message;
    } finally {
      if (queue.isCurrent(request)) {
        state.loading = false;
        if (queue.finish(request)) state.reloads += 1;
      }
    }
  };
  return { queue, state, read };
}

describe('log request ownership', () => {
  test('coalesces full refreshes while dropping overlapping polling ticks', () => {
    const queue = createLogRequestQueue();
    const request = queue.startRead(true)!;
    expect(queue.startRead(true)).toBeNull();
    expect(queue.startRead(false)).toBeNull();
    expect(queue.startRead(false)).toBeNull();
    expect(queue.finish(request)).toBe(true);
    expect(queue.finish(request)).toBe(false);
    const next = queue.startRead(false)!;
    expect(next).not.toBeNull();
    expect(queue.finish(next)).toBe(false);
  });

  test('clear invalidates old lines and cursor, and blocks reads until deletion settles', async () => {
    const { queue, state, read } = createReader();
    const old = deferred<{ lines: string[]; cursor: string }>();
    const reading = read(old.promise);
    queue.startRead(false); // A queued refresh before clear must also be discarded.
    const clearing = queue.startClear()!;
    state.lines = [];
    state.cursor = '';
    expect(queue.startClear()).toBeNull();
    expect(queue.startRead(true)).toBeNull();
    old.resolve({ lines: ['deleted log'], cursor: 'old-position' });
    await reading;
    expect(state.lines).toEqual([]);
    expect(state.cursor).toBe('');
    expect(state.reloads).toBe(0);
    expect(queue.isCurrent(clearing)).toBe(true);
    expect(queue.finish(clearing)).toBe(false);
  });

  test('a full refresh requested during clear runs only after clear finishes', () => {
    const queue = createLogRequestQueue();
    const clearing = queue.startClear()!;
    expect(queue.startRead(false)).toBeNull();
    expect(queue.finish(clearing)).toBe(true);
    const reading = queue.startRead(false)!;
    expect(reading).not.toBeNull();
    expect(queue.finish(reading)).toBe(false);
  });

  test('failed clear recovers the superseded read, but stale clear cannot trigger recovery', () => {
    const queue = createLogRequestQueue();
    const reading = queue.startRead(false)!;
    const clearing = queue.startClear()!;
    expect(queue.isCurrent(reading)).toBe(false);
    expect(queue.finish(clearing, true)).toBe(true);
    const next = queue.startRead(false)!;
    expect(queue.finish(clearing, true)).toBe(false);
    expect(queue.isCurrent(next)).toBe(true);
  });

  test('stale failures/finally cannot clear a newer loading state or drain its queue', async () => {
    const { queue, state, read } = createReader();
    const old = deferred<{ lines: string[]; cursor: string }>();
    const fresh = deferred<{ lines: string[]; cursor: string }>();
    const first = read(old.promise);
    queue.invalidate(); // Session/config change or effect cleanup.
    const second = read(fresh.promise);
    queue.startRead(false);
    old.reject(new Error('obsolete failure'));
    await first;
    expect(state.error).toBe('');
    expect(state.loading).toBe(true);
    expect(state.reloads).toBe(0);
    fresh.resolve({ lines: ['current'], cursor: 'current-position' });
    await second;
    expect(state.lines).toEqual(['current']);
    expect(state.cursor).toBe('current-position');
    expect(state.reloads).toBe(1);
  });

  test('cleanup discards queued work and allows StrictMode setup to start a fresh request', () => {
    const queue = createLogRequestQueue();
    const old = queue.startRead(false)!;
    queue.startRead(false);
    queue.invalidate();
    const fresh = queue.startRead(false)!;
    expect(fresh).not.toBeNull();
    expect(queue.isCurrent(old)).toBe(false);
    expect(queue.finish(old)).toBe(false);
    expect(queue.isCurrent(fresh)).toBe(true);
    expect(queue.finish(fresh)).toBe(false);
  });

  test('completed requests remain in their generation until invalidation', () => {
    const queue = createLogRequestQueue();
    const request = queue.startRead(true)!;
    queue.finish(request);
    expect(queue.isCurrent(request)).toBe(true);
    queue.invalidate();
    expect(queue.isCurrent(request)).toBe(false);
  });

  test('latest list/viewer request wins, and close/session changes invalidate pending work', () => {
    const guard = createLogRequestGuard();
    const first = guard.invalidate();
    const second = guard.invalidate();
    expect(guard.isCurrent(first)).toBe(false);
    expect(guard.isCurrent(second)).toBe(true);
    guard.invalidate();
    expect(guard.isCurrent(second)).toBe(false);
    const session = guard.capture();
    guard.invalidate();
    expect(guard.isCurrent(session)).toBe(false);
  });
});

describe('logs page lifecycle wiring', () => {
  const source = readFileSync(new URL('../src/pages/LogsPage.tsx', import.meta.url), 'utf8');

  test('invalidates synchronously on connection identity/config changes and unmount', () => {
    expect(source).toContain('useAuthStore.subscribe');
    expect(source).toContain('next.apiBase === previous.apiBase');
    expect(source).toContain('next.managementKey === previous.managementKey');
    expect(source).toContain('next.connectionStatus === previous.connectionStatus');
    expect(source).toContain('next.isAuthenticated === previous.isAuthenticated');
    expect(source).toContain('next.config?.loggingToFile !== previous.config?.loggingToFile');
    expect(source).toContain('next.config?.requestLog !== previous.config?.requestLog');
    expect(source).toContain(
      'unsubscribeAuth();\n      unsubscribeConfig();\n      invalidateSession();'
    );
  });

  test('queued reloads read current stores and delayed confirmations check their session', () => {
    expect(source).toContain("useAuthStore.getState().connectionStatus !== 'connected'");
    expect(source).toContain('!useConfigStore.getState().config?.loggingToFile');
    expect(source).toContain('if (!requests.session.isCurrent(session)) return;');
    expect(source).toContain('requests.logs.startClear()');
  });

  test('preserves bounded buffering, cursor reset, overlap merging and scroll integration', () => {
    expect(source).toContain('const MAX_BUFFER_LINES = 10000;');
    expect(source).toContain('const INITIAL_DISPLAY_LINES = 100;');
    expect(source).toContain('incremental && data.cursorReset');
    expect(source).toContain('mergeIncrementalLines(prev.buffer, newLines)');
    expect(source).toContain('useLogScroller({');
    // Ownership is checked before enqueueing, not inside a replayable React state updater.
    expect(source).not.toContain('if (!requests.logs.isCurrent(request)) return prev;');
  });
});
