import type { ConnectionPool, ProviderSource } from '@/types/provider';
import { isRecord } from '@/utils/helpers';

export function normalizeConnectionPool(raw: unknown): ConnectionPool | undefined {
  if (!isRecord(raw)) return undefined;
  const pool: ConnectionPool = {};
  if (typeof raw.enabled === 'boolean') pool.enabled = raw.enabled;
  const timeout = raw['idle-conn-timeout'];
  if (typeof timeout === 'string') pool.idleConnTimeout = timeout;
  const max = raw['max-idle-conns-per-host'];
  if (typeof max === 'number' && Number.isSafeInteger(max)) pool.maxIdleConnsPerHost = max;
  return pool;
}

export function serializeConnectionPool(config: {
  connectionPool?: ConnectionPool;
  source?: ProviderSource;
}): Record<string, unknown> {
  const pool = config.connectionPool;
  if (!pool) {
    // A null key-level value explicitly restores group inheritance in v8. Use
    // it in both comparisons so unrelated edits leave absent/null keys alone.
    return config.source?.keyIndex !== undefined && config.source.group['connection-pool'] != null
      ? { 'connection-pool': null }
      : {};
  }
  return {
    'connection-pool': {
      ...(pool.enabled !== undefined ? { enabled: pool.enabled } : {}),
      ...(pool.idleConnTimeout?.trim() ? { 'idle-conn-timeout': pool.idleConnTimeout.trim() } : {}),
      ...(pool.maxIdleConnsPerHost !== undefined
        ? { 'max-idle-conns-per-host': pool.maxIdleConnsPerHost }
        : {}),
    },
  };
}
