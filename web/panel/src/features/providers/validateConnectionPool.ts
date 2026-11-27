import { goDurationSeconds } from '@/features/config/visualConfigAdditions';
import type { ConnectionPool } from '@/types/provider';

export function validateConnectionPool(pool?: ConnectionPool, original?: ConnectionPool) {
  const max = pool?.maxIdleConnsPerHost;
  if (max !== undefined && !Number.isSafeInteger(max))
    return 'providersPage.nativeKey.invalidMaxIdle';
  const timeout = pool?.idleConnTimeout?.trim();
  if (
    timeout &&
    timeout !== original?.idleConnTimeout?.trim() &&
    goDurationSeconds(timeout) === undefined
  )
    return 'config_management.visual.validation.invalid_duration';
  return null;
}
