import { expect, test } from 'bun:test';
import {
  createResetGrantReader,
  scheduleResetGrantRead,
  RESET_GRANT_READ_DELAY_MS,
} from '../src/features/quota/providers/claude/resetGrantReads';
import type { AnthropicResetGrantStatus } from '../src/services/api/claudeResetGrants';

const status: AnthropicResetGrantStatus = {
  eligible: false,
  ineligibleReason: null,
  atLimit: false,
  grants: [],
  nextGrantId: null,
  weeklyResetsAt: null,
  cooldownUntil: null,
};
const deferred = () => {
  let resolve!: (value: AnthropicResetGrantStatus) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<AnthropicResetGrantStatus>((yes, no) => {
    resolve = yes;
    reject = no;
  });
  return { promise, resolve, reject };
};

test('waits 2000ms; cleanup cancels and stale sessions cannot dispatch', async () => {
  expect(RESET_GRANT_READ_DELAY_MS).toBe(2000);
  let reads = 0;
  let session = 1;
  const start = () => {
    reads += 1;
  };
  const cancel = scheduleResetGrantRead(start, () => true);
  const cancelStale = scheduleResetGrantRead(start, () => session === 1);
  const cancelActive = scheduleResetGrantRead(start, () => true);
  try {
    expect(reads).toBe(0);
    cancel(); // Effect cleanup: refresh, unmount, or connection change.
    session = 2; // Also guard a connection change before React cleanup runs.
    await Bun.sleep(1900);
    expect(reads).toBe(0);
    await Bun.sleep(200);
    expect(reads).toBe(1);
    await Bun.sleep(100);
    expect(reads).toBe(1);
  } finally {
    cancel();
    cancelStale();
    cancelActive();
  }
});

test('shares concurrent credential reads only within a session and never caches results', async () => {
  let revision = 1;
  const requests: ReturnType<typeof deferred>[] = [];
  const read = createResetGrantReader({
    revision: () => revision,
    read: () => {
      const request = deferred();
      requests.push(request);
      return request.promise;
    },
  });
  const old = read('a');
  expect(read('a') === old).toBe(true);
  const other = read('b');
  expect(requests).toHaveLength(2);
  revision = 2;
  const fresh = read('a');
  expect(fresh === old).toBe(false);
  const staleResult = old.catch((error: unknown) => error);
  requests[0].resolve(status);
  expect(await staleResult).toMatchObject({ message: 'session' });
  expect(read('a') === fresh).toBe(true); // Old cleanup must not delete the new session read.
  const otherResult = other.catch((error: unknown) => error);
  requests[1].resolve(status);
  expect(await otherResult).toMatchObject({ message: 'session' });
  requests[2].resolve(status);
  expect(await fresh).toBe(status);
  const next = read('a');
  expect(requests).toHaveLength(4);
  requests[3].resolve(status);
  await next;
});

test('failed reads are released without automatic retry', async () => {
  let calls = 0;
  const read = createResetGrantReader({
    revision: () => 1,
    read: async () => {
      calls += 1;
      throw new Error('rate limited');
    },
  });
  await expect(read('a')).rejects.toThrow('rate limited');
  expect(calls).toBe(1);
  await expect(read('a')).rejects.toThrow('rate limited');
  expect(calls).toBe(2);
});
