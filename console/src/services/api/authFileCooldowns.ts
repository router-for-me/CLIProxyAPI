import type { AuthFileCooldown, AuthFileCooldownSnapshot } from '@/types/authFile';
import { parseTimestampMs } from '@/utils/timestamp';

export function normalizeCooldownTimestamp(value: unknown): string | undefined {
  if (typeof value !== 'string' || !value.trim()) return undefined;
  const timestamp = parseTimestampMs(value);
  return Number.isFinite(timestamp) ? new Date(timestamp).toISOString() : undefined;
}

function normalizeRecord(value: unknown): AuthFileCooldown | null {
  if (!value || typeof value !== 'object') return null;
  const entry = value as Record<string, unknown>;
  const scope = entry.scope;
  if (scope !== 'model' && scope !== 'credential') return null;
  const modelKey = typeof entry.model_key === 'string' ? entry.model_key.trim() : '';
  if (scope === 'model' && !modelKey) return null;
  const retryAt = normalizeCooldownTimestamp(entry.retry_at);
  const remainingSeconds = entry.remaining_seconds;
  if (
    !retryAt ||
    typeof remainingSeconds !== 'number' ||
    !Number.isSafeInteger(remainingSeconds) ||
    remainingSeconds <= 0 ||
    remainingSeconds > Number.MAX_SAFE_INTEGER / 1000
  ) {
    return null;
  }
  const reason = typeof entry.reason === 'string' ? entry.reason.trim() : '';
  const backoffLevel = entry.backoff_level;
  const httpStatus = entry.http_status;
  return {
    scope,
    ...(scope === 'model' ? { modelKey } : {}),
    reason: reason || 'unknown',
    retryAt,
    remainingSeconds,
    ...(typeof backoffLevel === 'number' && Number.isSafeInteger(backoffLevel) && backoffLevel >= 0
      ? { backoffLevel }
      : {}),
    ...(typeof httpStatus === 'number' &&
    Number.isInteger(httpStatus) &&
    httpStatus >= 400 &&
    httpStatus <= 599
      ? { httpStatus }
      : {}),
  };
}

export function normalizeAuthFileCooldowns(
  value: unknown,
  observedAt: string | undefined,
  receivedAtMs: number
): AuthFileCooldownSnapshot | undefined {
  if (value === undefined) return undefined;
  const snapshot = { observedAt, receivedAtMs };
  if (!Array.isArray(value)) return { ...snapshot, records: null };
  const records = value.map(normalizeRecord);
  // A malformed/future scope must not silently turn a restriction into "no timers".
  if (records.some((record) => record === null)) return { ...snapshot, records: null };
  return { ...snapshot, records: records as AuthFileCooldown[] };
}
