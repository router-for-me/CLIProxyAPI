import type { AuthFileCooldown, AuthFileCooldownSnapshot } from '@/types/authFile';

const REASON_KEYS: Record<string, string> = {
  quota: 'auth_files.cooldown_reason_quota',
  credential_quota: 'auth_files.cooldown_reason_credential_quota',
  cloudflare_challenge: 'auth_files.cooldown_reason_cloudflare_challenge',
  invalid_grant: 'auth_files.cooldown_reason_invalid_grant',
  unauthorized: 'auth_files.cooldown_reason_unauthorized',
  payment_required: 'auth_files.cooldown_reason_payment_required',
  not_found: 'auth_files.cooldown_reason_not_found',
  model_not_supported: 'auth_files.cooldown_reason_model_not_supported',
  transient_error: 'auth_files.cooldown_reason_transient_error',
};

export function cooldownReasonKey(reason: string): string {
  return Object.prototype.hasOwnProperty.call(REASON_KEYS, reason)
    ? REASON_KEYS[reason]
    : 'auth_files.cooldown_reason_unknown';
}

export function cooldownRemainingSeconds(
  record: AuthFileCooldown,
  receivedAtMs: number,
  nowMs: number
): number {
  // remaining_seconds is measured by the server. Never subtract a local wall clock
  // from retry_at, or infer a deadline from backoff_level. Clamp pre-receipt ticks.
  const elapsedMs = Math.max(0, nowMs - receivedAtMs);
  return Math.max(0, Math.ceil(record.remainingSeconds - elapsedMs / 1000));
}

export function summarizeCooldowns(snapshot: AuthFileCooldownSnapshot, nowMs: number) {
  const rows = (snapshot.records ?? []).map((record) => ({
    record,
    remainingSeconds: cooldownRemainingSeconds(record, snapshot.receivedAtMs, nowMs),
  }));
  const active = rows.filter((row) => row.remainingSeconds > 0);
  return {
    rows,
    modelCount: active.filter((row) => row.record.scope === 'model').length,
    credentialWide: active.some((row) => row.record.scope === 'credential'),
    earliestSeconds: active.length ? Math.min(...active.map((row) => row.remainingSeconds)) : 0,
  };
}
