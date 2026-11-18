import { describe, expect, test } from 'bun:test';
import {
  createDevinQuotaFetcher,
  DevinQuotaError,
  type DevinRequestGeneration,
} from '@/features/quota/providers/devin/requests';
import type { ApiCallRequest, ApiCallResult } from '@/services/api/apiCall';
import type { AuthFileItem, DevinQuotaData } from '@/types';

type Deferred<T> = {
  promise: Promise<T>;
  resolve: (value: T) => void;
  reject: (reason?: unknown) => void;
};

const deferred = <T>(): Deferred<T> => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

const file = (
  name: string,
  authIndex: string | number | undefined,
  extra: Record<string, unknown> = {}
): AuthFileItem => ({
  name,
  provider: 'devin',
  authIndex,
  ...extra,
});

const liveBody = (overrides: Record<string, unknown> = {}) => ({
  userStatus: {
    planStatus: {
      dailyQuotaRemainingPercent: 40,
      dailyQuotaResetAtUnix: '1777680000',
      weeklyQuotaRemainingPercent: '75',
      weeklyQuotaResetAtUnix: 1778198400,
      planInfo: { planName: 'pro' },
      planStart: '2026-05-01T00:00:00Z',
      planEnd: '2026-06-01T00:00:00Z',
      ...overrides,
    },
  },
});

const result = (statusCode = 200, body: unknown = liveBody()): ApiCallResult => ({
  statusCode,
  header: {},
  bodyText: body === null ? '' : JSON.stringify(body),
  body,
});

const expectCode = async (promise: Promise<unknown>, code: string) => {
  try {
    await promise;
    throw new Error('expected request to reject');
  } catch (error) {
    expect(error).toBeInstanceOf(DevinQuotaError);
    expect((error as DevinQuotaError).code).toBe(code);
  }
};

const stableGeneration = (): DevinRequestGeneration => ({ session: 1, file: 1 });

const successfulFetcher = (
  request: (payload: ApiCallRequest) => Promise<ApiCallResult> = async () => result(),
  generation: (name: string) => DevinRequestGeneration = stableGeneration
) => createDevinQuotaFetcher({ request, generation });

describe('createDevinQuotaFetcher', () => {
  test('sends the exact Connect RPC request and returns only normalized live values', async () => {
    const requests: ApiCallRequest[] = [];
    const secret = 'credential-that-must-not-leak';
    const before = Date.now();
    const fetchQuota = successfulFetcher(async (payload) => {
      requests.push(payload);
      return result();
    });

    const quota = await fetchQuota(
      file(' account.json ', ' 007 ', {
        access_token: secret,
        metadata: { apiKey: secret },
        quota: {
          signals: {
            daily_quota_remaining_percent: 1,
            plan: secret,
          },
        },
      })
    );
    const after = Date.now();

    expect(requests).toHaveLength(1);
    expect(requests[0]).toEqual({
      authIndex: '007',
      method: 'POST',
      url: 'https://server.codeium.com/exa.seat_management_pb.SeatManagementService/GetUserStatus',
      header: {
        'Content-Type': 'application/json',
        'Connect-Protocol-Version': '1',
      },
      data: JSON.stringify({
        metadata: {
          ideName: 'chisel',
          ideVersion: '3000.10.21',
          apiKey: '$TOKEN$',
          locale: 'en',
          os: 'darwin',
          extensionVersion: '3000.10.21',
          clientName: 'chisel',
        },
      }),
    });
    expect(requests[0]!.header).not.toHaveProperty('Authorization');
    expect(requests[0]!.header).not.toHaveProperty('X-Api-Key');
    expect(quota).toEqual<DevinQuotaData>({
      windows: [
        {
          id: 'daily',
          remainingPercent: 40,
          resetAtMs: 1777680000 * 1000,
          periodHours: 24,
        },
        {
          id: 'weekly',
          remainingPercent: 75,
          resetAtMs: 1778198400 * 1000,
          periodHours: 168,
        },
      ],
      observedAtMs: expect.any(Number) as unknown as number,
      plan: 'pro',
      planStartMs: Date.parse('2026-05-01T00:00:00Z'),
      planEndMs: Date.parse('2026-06-01T00:00:00Z'),
    });
    expect(quota.observedAtMs).toBeGreaterThanOrEqual(before);
    expect(quota.observedAtMs).toBeLessThanOrEqual(after);
    expect(JSON.stringify(quota)).not.toContain(secret);
  });

  test('accepts the same live quota response on repeated refreshes', async () => {
    let calls = 0;
    const fetchQuota = successfulFetcher(async () => {
      calls += 1;
      return result();
    });
    const authFile = file('repeat.json', '7');

    const first = await fetchQuota(authFile);
    await Promise.resolve();
    const second = await fetchQuota(authFile);

    expect(first.windows).toEqual(second.windows);
    expect(calls).toBe(2);
  });

  test('deduplicates an in-flight auth identity but not a different auth index', async () => {
    const gates = [deferred<ApiCallResult>(), deferred<ApiCallResult>()];
    const seenIndexes: string[] = [];
    const fetchQuota = successfulFetcher((payload) => {
      seenIndexes.push(payload.authIndex!);
      return gates[seenIndexes.length - 1]!.promise;
    });

    const first = fetchQuota(file(' shared.json ', 3));
    const duplicate = fetchQuota(file('shared.json', ' 3 '));
    const otherIdentity = fetchQuota(file('shared.json', '4'));

    expect(duplicate).toBe(first);
    expect(otherIdentity).not.toBe(first);
    expect(seenIndexes).toEqual(['3', '4']);
    gates.forEach((gate) => gate.resolve(result()));
    const [firstResult, duplicateResult] = await Promise.all([first, duplicate, otherIdentity]);
    expect(firstResult).toBe(duplicateResult);
  });

  test('limits requests to three concurrent auth identities', async () => {
    const gates = Array.from({ length: 5 }, () => deferred<ApiCallResult>());
    let started = 0;
    const fetchQuota = successfulFetcher(() => gates[started++]!.promise);

    const requests = Array.from({ length: 5 }, (_, index) =>
      fetchQuota(file(`file-${index}.json`, String(index)))
    );
    expect(started).toBe(3);

    gates[0]!.resolve(result());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(started).toBe(4);

    gates[1]!.resolve(result());
    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(started).toBe(5);

    gates.slice(2).forEach((gate) => gate.resolve(result()));
    await Promise.all(requests);
  });

  test('does not let saturated old-session requests block a new session', async () => {
    const oldGates = Array.from({ length: 3 }, () => deferred<ApiCallResult>());
    const started: string[] = [];
    let oldIndex = 0;
    const fetchQuota = successfulFetcher(
      (payload) => {
        const index = payload.authIndex!;
        started.push(index);
        return index.startsWith('old-') ? oldGates[oldIndex++]!.promise : Promise.resolve(result());
      },
      (name) => ({ session: name.startsWith('old-') ? 1 : 2, file: 0 })
    );

    const oldRequests = Array.from({ length: 3 }, (_, index) =>
      fetchQuota(file(`old-${index}.json`, `old-${index}`))
    );
    const currentRequest = fetchQuota(file('current.json', 'current'));

    expect(started).toContain('current');
    await expect(currentRequest).resolves.toMatchObject({ plan: 'pro' });
    oldGates.forEach((gate) => gate.resolve(result()));
    await Promise.all(oldRequests);
  });

  test('checks session and file generations before a queued request executes', async () => {
    const blockers = Array.from({ length: 3 }, () => deferred<ApiCallResult>());
    const generations: Record<string, DevinRequestGeneration> = {};
    const requested: string[] = [];
    let blockerIndex = 0;
    const fetchQuota = successfulFetcher(
      (payload) => {
        requested.push(payload.authIndex!);
        return payload.authIndex!.startsWith('block')
          ? blockers[blockerIndex++]!.promise
          : Promise.resolve(result());
      },
      (name) => generations[name] ?? { session: 1, file: 1 }
    );

    const active = Array.from({ length: 3 }, (_, index) =>
      fetchQuota(file(`block-${index}.json`, `block-${index}`))
    );
    const staleSession = fetchQuota(file('stale-session.json', 'stale-session'));
    const staleFile = fetchQuota(file('stale-file.json', 'stale-file'));
    generations['stale-session.json'] = { session: 2, file: 1 };
    generations['stale-file.json'] = { session: 1, file: 2 };

    blockers[0]!.resolve(result());
    blockers[1]!.resolve(result());
    await expectCode(staleSession, 'stale_request');
    await expectCode(staleFile, 'stale_request');
    expect(requested).not.toContain('stale-session');
    expect(requested).not.toContain('stale-file');

    blockers[2]!.resolve(result());
    await Promise.all(active);
  });

  test('checks session and file generations after the response', async () => {
    for (const changed of ['session', 'file'] as const) {
      const gate = deferred<ApiCallResult>();
      const generation = { session: 4, file: 8 };
      const fetchQuota = successfulFetcher(
        () => gate.promise,
        () => ({ ...generation })
      );
      const request = fetchQuota(file(`${changed}.json`, changed));

      generation[changed] += 1;
      gate.resolve(result());
      await expectCode(request, 'stale_request');
    }
  });

  test('rejects missing names and auth indexes before making a request', async () => {
    let calls = 0;
    const fetchQuota = successfulFetcher(async () => {
      calls += 1;
      return result();
    });

    await expectCode(fetchQuota(file(' ', '1')), 'missing_identity');
    await expectCode(fetchQuota(file('valid.json', ' ')), 'missing_identity');
    await expectCode(fetchQuota(file('valid.json', undefined)), 'missing_identity');
    expect(calls).toBe(0);
  });

  test('rejects unsuccessful statuses with the upstream status and message', async () => {
    const fetchQuota = successfulFetcher(async () =>
      result(429, { error: { message: 'quota endpoint throttled' } })
    );

    try {
      await fetchQuota(file('status.json', '1'));
      throw new Error('expected request to reject');
    } catch (error) {
      expect(error).toBeInstanceOf(Error);
      expect((error as Error).message).toBe('429 quota endpoint throttled');
      expect((error as Error & { status?: number }).status).toBe(429);
    }
  });

  test('rejects successful responses without live quota data', async () => {
    const fetchQuota = successfulFetcher(async () => result(200, { userStatus: {} }));
    await expectCode(fetchQuota(file('empty.json', '1')), 'empty_data');
  });

  test('propagates request failures and allows the identity to retry', async () => {
    const failure = new Error('request exploded');
    let attempts = 0;
    const fetchQuota = successfulFetcher(async () => {
      attempts += 1;
      if (attempts === 1) throw failure;
      return result();
    });
    const authFile = file('retry.json', '1');

    await expect(fetchQuota(authFile)).rejects.toBe(failure);
    await Promise.resolve();
    await expect(fetchQuota(authFile)).resolves.toMatchObject({ plan: 'pro' });
    expect(attempts).toBe(2);
  });

  test('ignores generation changes belonging to an unrelated file', async () => {
    const gate = deferred<ApiCallResult>();
    const generations: Record<string, DevinRequestGeneration> = {
      'target.json': { session: 3, file: 4 },
      'other.json': { session: 3, file: 7 },
    };
    const fetchQuota = successfulFetcher(
      () => gate.promise,
      (name) => generations[name]!
    );

    const request = fetchQuota(file('target.json', '5'));
    generations['other.json']!.file += 1;
    gate.resolve(result());

    await expect(request).resolves.toMatchObject({ plan: 'pro' });
  });
});
