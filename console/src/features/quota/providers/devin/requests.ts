import type { AuthFileItem, DevinQuotaData } from '@/types';
import {
  getApiCallErrorMessage,
  type ApiCallRequest,
  type ApiCallResult,
} from '@/services/api/apiCall';
import { createStatusError } from '@/utils/quota';
import { hasDevinQuotaObservation, readDevinQuotaResponse } from '@/services/api/devinQuota';
import { normalizeAuthIndex } from '@/utils/authIndex';

export type DevinQuotaErrorCode = 'missing_identity' | 'stale_request' | 'empty_data';

export class DevinQuotaError extends Error {
  constructor(public readonly code: DevinQuotaErrorCode) {
    super(code);
    this.name = 'DevinQuotaError';
  }
}

export interface DevinRequestGeneration {
  session: number;
  file: number;
}

interface DevinQuotaDependencies {
  request: (payload: ApiCallRequest) => Promise<ApiCallResult>;
  generation: (name: string) => DevinRequestGeneration;
}

/**
 * Share in-flight work between card/page/batch entry points. Limit upstream calls
 * to three at once. Every queued job and upstream response recheck both
 * session and file generations, so old jobs cannot run against a new connection.
 */
export function createDevinQuotaFetcher(deps: DevinQuotaDependencies) {
  const inFlight = new Map<string, Promise<DevinQuotaData>>();
  const pools = new Map<number, { queue: Array<() => void>; active: number }>();

  const getPool = (session: number) => {
    const existing = pools.get(session);
    if (existing) return existing;
    const pool = { queue: [] as Array<() => void>, active: 0 };
    pools.set(session, pool);
    return pool;
  };

  const runNext = (session: number) => {
    const pool = pools.get(session);
    if (!pool) return;
    while (pool.active < 3 && pool.queue.length > 0) {
      pool.active += 1;
      pool.queue.shift()!();
    }
    if (pool.active === 0 && pool.queue.length === 0) pools.delete(session);
  };

  return (file: AuthFileItem): Promise<DevinQuotaData> => {
    const name = file.name.trim();
    const authIndex = normalizeAuthIndex(file.authIndex ?? file.auth_index);
    if (!name || !authIndex) return Promise.reject(new DevinQuotaError('missing_identity'));
    const generation = deps.generation(name);
    const key = JSON.stringify([generation.session, generation.file, name, authIndex]);
    const existing = inFlight.get(key);
    if (existing) return existing;

    const assertCurrent = () => {
      const current = deps.generation(name);
      if (current.session !== generation.session || current.file !== generation.file) {
        throw new DevinQuotaError('stale_request');
      }
    };
    const pool = getPool(generation.session);
    const request = new Promise<DevinQuotaData>((resolve, reject) => {
      pool.queue.push(() => {
        const execute = async () => {
          assertCurrent();
          const response = await deps.request({
            authIndex,
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
          assertCurrent();
          if (response.statusCode < 200 || response.statusCode >= 300) {
            throw createStatusError(getApiCallErrorMessage(response), response.statusCode);
          }
          const quota = readDevinQuotaResponse(response.body);
          if (!hasDevinQuotaObservation(quota)) throw new DevinQuotaError('empty_data');
          return quota;
        };
        const finish = () => {
          inFlight.delete(key);
          pool.active -= 1;
          runNext(generation.session);
        };
        void execute().then(
          (quota) => {
            finish();
            resolve(quota);
          },
          (error: unknown) => {
            finish();
            reject(error);
          }
        );
      });
    });
    inFlight.set(key, request);
    runNext(generation.session);
    return request;
  };
}
