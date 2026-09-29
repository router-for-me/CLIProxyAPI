import { describe, expect, test } from 'bun:test';
import { createOAuthAttempts } from '../src/pages/oauthAttempts';

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function setup() {
  let nextId = 0;
  const tasks = new Map<number, { callback: () => void; delay: number }>();
  const attempts = createOAuthAttempts({
    setTimeout: (callback, delay) => {
      tasks.set(++nextId, { callback, delay });
      return nextId;
    },
    clearTimeout: (id) => {
      tasks.delete(id);
    },
  });
  const tick = () => {
    const pending = [...tasks.entries()];
    for (const [id, task] of pending) {
      tasks.delete(id);
      task.callback();
    }
  };
  return { attempts, tasks, tick };
}

const flush = async () => {
  await Promise.resolve();
  await Promise.resolve();
};

describe('OAuth attempt lifecycle', () => {
  test('waits for each request to settle before scheduling another poll', async () => {
    const { attempts, tasks, tick } = setup();
    const response = deferred<string>();
    let requests = 0;
    const results: string[] = [];
    attempts.begin('codex').poll(
      () => {
        requests++;
        return response.promise;
      },
      (result) => {
        results.push(result);
        return result === 'wait';
      },
      () => {},
      3000
    );
    expect([...tasks.values()][0].delay).toBe(3000);
    tick();
    tick();
    tick();
    expect(requests).toBe(1);
    expect(tasks.size).toBe(0);
    response.resolve('wait');
    await flush();
    expect(results).toEqual(['wait']);
    expect(tasks.size).toBe(1);
    tick();
    expect(requests).toBe(2);
  });

  for (const outcome of ['success', 'error'] as const) {
    test(`ignores old ${outcome} without stopping the next attempt`, async () => {
      const { attempts, tasks, tick } = setup();
      const response = deferred<string>();
      let effects = 0;
      const old = attempts.begin('codex');
      old.poll(
        () => response.promise,
        () => {
          effects++;
          attempts.begin('codex');
          return false;
        },
        () => effects++,
        3000
      );
      tick();
      const current = attempts.begin('codex');
      current.schedule(() => {}, 3000);
      if (outcome === 'success') response.resolve('ok');
      else response.reject(new Error('late timeout'));
      await flush();
      expect(effects).toBe(0);
      expect(current.isCurrent()).toBe(true);
      expect(tasks.size).toBe(1);
      old.invalidate();
      expect(current.isCurrent()).toBe(true);
      expect(tasks.size).toBe(1);
    });
  }

  test('cleanup makes in-flight responses inert and cancels all provider timers', async () => {
    const { attempts, tasks, tick } = setup();
    const response = deferred<string>();
    let effects = 0;
    const old = attempts.begin('codex');
    old.poll(
      () => response.promise,
      () => {
        effects++;
        old.schedule(() => effects++, 5000);
        return true;
      },
      () => effects++,
      3000
    );
    tick();
    attempts.begin('anthropic').schedule(() => effects++, 3000);
    attempts.invalidateAll();
    response.resolve('ok');
    await flush();
    expect(old.isCurrent()).toBe(false);
    expect(old.signal.aborted).toBe(true);
    expect(tasks.size).toBe(0);
    expect(effects).toBe(0);
    expect(attempts.begin('codex').isCurrent()).toBe(true);
  });

  test('new login cancels success reset without affecting other providers', () => {
    const { attempts, tasks, tick } = setup();
    let resets = 0;
    const codex = attempts.begin('codex');
    codex.schedule(() => resets++, 5000);
    const anthropic = attempts.begin('anthropic');
    anthropic.schedule(() => resets++, 5000);
    const next = attempts.begin('codex');
    expect(codex.isCurrent()).toBe(false);
    expect(anthropic.isCurrent()).toBe(true);
    expect(tasks.size).toBe(1);
    tick();
    expect(resets).toBe(1);
    expect(next.isCurrent()).toBe(true);
  });

  test('Devin cancellation invalidates an in-flight poll and callback before DELETE settles', async () => {
    const { attempts, tasks, tick } = setup();
    const response = deferred<string>();
    const callback = deferred<void>();
    let effects = 0;
    const login = attempts.begin('devin');
    login.poll(
      () => response.promise,
      () => {
        effects++;
        return true;
      },
      () => effects++,
      3000
    );
    const submission = callback.promise.then(() => {
      if (login.isCurrent()) effects++;
    });
    tick();
    const cancellation = attempts.begin('devin');
    response.resolve('ok');
    callback.resolve();
    await submission;
    await flush();
    expect(effects).toBe(0);
    expect(tasks.size).toBe(0);
    expect(cancellation.isCurrent()).toBe(true);
    expect(login.signal.aborted).toBe(true);
    expect(cancellation.signal.aborted).toBe(false);

    // A connection switch or a later login also makes an outstanding DELETE inert.
    attempts.invalidateAll();
    const next = attempts.begin('devin');
    expect(cancellation.isCurrent()).toBe(false);
    expect(cancellation.signal.aborted).toBe(true);
    cancellation.invalidate();
    expect(next.isCurrent()).toBe(true);
    expect(next.signal.aborted).toBe(false);
  });

  test('Devin terminal status stops polling without disturbing another provider', async () => {
    for (const status of ['ok', 'error']) {
      const { attempts, tasks, tick } = setup();
      const other = attempts.begin('codex');
      other.schedule(() => {}, 3000);
      const devin = attempts.begin('devin');
      const results: string[] = [];
      devin.poll(
        async () => status,
        (result) => {
          results.push(result);
          devin.invalidate();
          return false;
        },
        () => {},
        3000
      );
      tick();
      await flush();
      expect(results).toEqual([status]);
      expect(tasks.size).toBe(0);
      expect(other.isCurrent()).toBe(true);
      expect(devin.isCurrent()).toBe(false);
    }
  });

  test('terminal results and request errors do not schedule another poll', async () => {
    for (const fail of [false, true]) {
      const { attempts, tasks, tick } = setup();
      let results = 0;
      let errors = 0;
      attempts.begin('codex').poll(
        async () => {
          if (fail) throw new Error('timeout');
          return 'ok';
        },
        () => {
          results++;
          return false;
        },
        () => errors++,
        3000
      );
      tick();
      await flush();
      expect(results).toBe(fail ? 0 : 1);
      expect(errors).toBe(fail ? 1 : 0);
      expect(tasks.size).toBe(0);
    }
  });
});
