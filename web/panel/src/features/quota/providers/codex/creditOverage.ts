/**
 * Codex account-level "use credits after the usage limit" setting
 * (`can_consume_personal_credits_chatpass`).
 *
 * Issue #551 reports a write-only account-user setting. This feature never reads or
 * probes it: a value is known only after a write has been confirmed in this session.
 * React-free; tests/codexCreditOverage.test.ts consumes it.
 */

import type { TFunction } from 'i18next';
import {
  captureApiCallApi,
  apiClient,
  getApiCallErrorMessage,
  type ApiCallRequest,
} from '@/services/api';
import { useQuotaStore } from '@/stores/useQuotaStore';
import type {
  AuthFileItem,
  CodexCreditOverageEntry,
  CodexCreditOverageOperation,
  CodexQuotaState,
} from '@/types';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { isRecord } from '@/utils/helpers';
import {
  CODEX_ACCOUNT_USER_SETTING_URL,
  CODEX_CREDIT_OVERAGE_FEATURE,
  formatInstantShort,
  getStatusFromError,
  resolveCodexChatgptAccountId,
} from '@/utils/quota';
import { buildCodexRequestHeader } from './data';

export type CodexCreditOverageErrorCode =
  | 'missing_auth_index'
  | 'missing_account_id'
  | 'unauthorized'
  | 'forbidden'
  | 'unsupported'
  | 'rate_limited'
  | 'unconfirmed'
  | 'opposite_pending'
  | 'failed';

/** `unconfirmed` is the only code whose write may already have taken effect. */
export class CodexCreditOverageError extends Error {
  readonly code: CodexCreditOverageErrorCode;
  readonly status?: number;

  constructor(code: CodexCreditOverageErrorCode, message: string = code, status?: number) {
    super(message);
    this.name = 'CodexCreditOverageError';
    this.code = code;
    this.status = status;
  }
}

export type CodexCreditOverageState = 'unknown' | 'writing' | 'allowed' | 'blocked' | 'unconfirmed';

export function resolveCodexCreditOverageState(
  entry: CodexCreditOverageEntry | undefined
): CodexCreditOverageState {
  if (!entry) return 'unknown';
  if (entry.status === 'known') return entry.value ? 'allowed' : 'blocked';
  return entry.status;
}

/**
 * Knowledge only lives for the session, which rarely crosses midnight, so the date is
 * shown only when it does; the time alone keeps the card row on one line.
 */
export function formatCodexCreditOverageConfirmedAt(at: number, now: number = Date.now()): string {
  const confirmed = new Date(at);
  const current = new Date(now);
  const sameDay =
    confirmed.getFullYear() === current.getFullYear() &&
    confirmed.getMonth() === current.getMonth() &&
    confirmed.getDate() === current.getDate();
  if (!sameDay) return formatInstantShort(at);
  return confirmed.toLocaleTimeString(undefined, {
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

/**
 * Account-user identity. Team workspaces share one ChatGPT account ID across members
 * while this is an account-user setting, so the email narrows the key. Without an
 * email a credential shares its state with nobody rather than with the wrong user.
 */
export function codexCreditOverageKey(file: AuthFileItem): string | null {
  const accountId = resolveCodexChatgptAccountId(file);
  if (!accountId) return null;
  const email = typeof file.email === 'string' ? file.email.trim().toLowerCase() : '';
  return email ? `${accountId}|email:${email}` : `${accountId}|file:${file.name}`;
}

/** A missing user identity must never create a second lock for the same account. */
export function codexCreditOverageWriteBlocked(
  operation: CodexCreditOverageOperation | undefined,
  value: boolean
): boolean {
  return (
    operation?.status === 'writing' ||
    (operation?.status === 'unconfirmed' && operation.target !== value)
  );
}

/** Every card in the account observes its mutex and any unresolved write. */
export function codexCreditOverageDisplayEntry(
  entry: CodexCreditOverageEntry | undefined,
  operation: CodexCreditOverageOperation | undefined
): CodexCreditOverageEntry | undefined {
  if (operation?.status === 'writing') return { status: 'writing', target: operation.target };
  if (operation?.status === 'unconfirmed') return operation;
  return entry;
}

/** How many credentials share each account user's setting. */
export function countCodexCreditOverageKeys(files: readonly AuthFileItem[]): Map<string, number> {
  const counts = new Map<string, number>();
  files.forEach((file) => {
    const key = codexCreditOverageKey(file);
    if (key) counts.set(key, (counts.get(key) ?? 0) + 1);
  });
  return counts;
}

export function buildCodexCreditOverageRequest(file: AuthFileItem, value: boolean): ApiCallRequest {
  const authIndex = normalizeAuthIndex(file['auth_index'] ?? file.authIndex);
  if (!authIndex) throw new CodexCreditOverageError('missing_auth_index');
  if (!resolveCodexChatgptAccountId(file)) {
    throw new CodexCreditOverageError('missing_account_id');
  }
  const query = new URLSearchParams({
    feature: CODEX_CREDIT_OVERAGE_FEATURE,
    value: String(value),
  });
  // The whole payload lives in the query string. The backend substitutes $TOKEN$ with
  // the outer Codex access token; the nested SIWC token is rejected by this endpoint.
  return {
    authIndex,
    method: 'PATCH',
    url: `${CODEX_ACCOUNT_USER_SETTING_URL}?${query.toString()}`,
    header: buildCodexRequestHeader(file),
  };
}

/** The write response is the only source of the value; accept real booleans only. */
export function parseCodexCreditOverageResponse(body: unknown): boolean | null {
  if (!isRecord(body)) return null;
  const value = body[CODEX_CREDIT_OVERAGE_FEATURE];
  return typeof value === 'boolean' ? value : null;
}

const codeForUpstreamStatus = (status: number): CodexCreditOverageErrorCode => {
  if (status === 401) return 'unauthorized';
  if (status === 403) return 'forbidden';
  if (status === 404 || status === 405) return 'unsupported';
  if (status === 429) return 'rate_limited';
  if (status >= 400 && status < 500) return 'failed';
  return 'unconfirmed';
};

/** Send one write and return the value ChatGPT confirmed. */
export async function sendCodexCreditOverage(
  request: ApiCallRequest,
  value: boolean,
  api = captureApiCallApi()
): Promise<boolean> {
  let result;
  try {
    result = await api.request(request);
  } catch (err: unknown) {
    const status = getStatusFromError(err);
    const message = err instanceof Error ? err.message : String(err);
    // A management 4xx rejected the call before it was forwarded (for example when the
    // credential token cannot be resolved). Anything else may have reached ChatGPT.
    const forwarded = status === undefined || status < 400 || status >= 500;
    throw new CodexCreditOverageError(forwarded ? 'unconfirmed' : 'failed', message, status);
  }

  if (result.statusCode >= 200 && result.statusCode < 300) {
    if (parseCodexCreditOverageResponse(result.body) === value) return value;
    throw new CodexCreditOverageError(
      'unconfirmed',
      getApiCallErrorMessage(result),
      result.statusCode
    );
  }
  throw new CodexCreditOverageError(
    codeForUpstreamStatus(result.statusCode),
    getApiCallErrorMessage(result),
    result.statusCode
  );
}

export type CodexCreditOverageOutcome =
  | { kind: 'applied'; value: boolean }
  | { kind: 'unconfirmed' }
  | { kind: 'failed'; error: CodexCreditOverageError }
  | { kind: 'busy' }
  | { kind: 'stale' };

const toCodexCreditOverageError = (err: unknown): CodexCreditOverageError =>
  err instanceof CodexCreditOverageError
    ? err
    : new CodexCreditOverageError('unconfirmed', err instanceof Error ? err.message : String(err));

/**
 * Write the setting for the file's account user and record what is known afterwards.
 * The account mutex is independent of the account-user snapshot. An ambiguous write
 * remains unresolved for this session, even after a same-value retry succeeds: an
 * earlier request may still arrive later. Opposite writes must not overtake it.
 */
export async function applyCodexCreditOverage(
  file: AuthFileItem,
  value: boolean
): Promise<CodexCreditOverageOutcome> {
  let request: ApiCallRequest;
  try {
    request = buildCodexCreditOverageRequest(file, value);
  } catch (err: unknown) {
    return { kind: 'failed', error: toCodexCreditOverageError(err) };
  }
  const key = codexCreditOverageKey(file);
  const accountId = resolveCodexChatgptAccountId(file);
  if (!key || !accountId) {
    return { kind: 'failed', error: new CodexCreditOverageError('missing_account_id') };
  }

  const store = useQuotaStore.getState();
  const previous = store.codexCreditOverage[key];
  const operation = store.codexCreditOverageOperations[accountId];
  if (operation?.status === 'writing') return { kind: 'busy' };
  if (codexCreditOverageWriteBlocked(operation, value)) {
    return { kind: 'failed', error: new CodexCreditOverageError('opposite_pending') };
  }
  const generation = store.cacheGeneration;
  const revision = apiClient.getConnectionRevision();
  const isCurrent = () =>
    useQuotaStore.getState().cacheGeneration === generation &&
    apiClient.getConnectionRevision() === revision;
  // Capture before notifying subscribers; neither request routing nor old response
  // events may inherit a replacement connection or session.
  const api = captureApiCallApi(isCurrent);
  const accountWriting: CodexCreditOverageOperation = {
    status: 'writing',
    target: value,
    unconfirmed: operation?.status === 'unconfirmed',
  };
  const writing: CodexCreditOverageEntry = { status: 'writing', target: value };
  store.setCodexCreditOverageOperation(accountId, accountWriting);
  // Missing emails cannot prove which other credential belongs to the same user.
  // Conservatively expire this account's other snapshots rather than leave a
  // contradictory confirmed value on another card.
  Object.keys(store.codexCreditOverage).forEach((otherKey) => {
    if (otherKey !== key && otherKey.startsWith(`${accountId}|`)) {
      store.setCodexCreditOverage(otherKey, undefined);
    }
  });
  store.setCodexCreditOverage(key, writing);

  const settle = (
    next: CodexCreditOverageEntry | undefined,
    nextOperation?: CodexCreditOverageOperation
  ): boolean => {
    const current = useQuotaStore.getState();
    const active = isCurrent();
    // Release only our own marker; a stale answer must not leak into the new session.
    if (current.codexCreditOverageOperations[accountId] !== accountWriting) return false;
    if (current.codexCreditOverage[key] === writing) {
      current.setCodexCreditOverage(key, active ? next : undefined);
    }
    // A quota-only reset forgets observations, not the possibility of a late write.
    const retained =
      !active && apiClient.getConnectionRevision() === revision
        ? { status: 'unconfirmed' as const, target: value, at: Date.now() }
        : undefined;
    current.setCodexCreditOverageOperation(accountId, active ? nextOperation : retained);
    return active;
  };

  try {
    const confirmed = await sendCodexCreditOverage(request, value, api);
    if (accountWriting.unconfirmed) {
      const unresolved = { status: 'unconfirmed' as const, target: value, at: Date.now() };
      if (!settle(unresolved, unresolved)) return { kind: 'stale' };
      return { kind: 'unconfirmed' };
    }
    if (!settle({ status: 'known', value: confirmed, at: Date.now() })) return { kind: 'stale' };
    return { kind: 'applied', value: confirmed };
  } catch (err: unknown) {
    const error = toCodexCreditOverageError(err);
    const unresolved =
      error.code === 'unconfirmed' || accountWriting.unconfirmed
        ? { status: 'unconfirmed' as const, target: value, at: Date.now() }
        : undefined;
    if (!settle(unresolved ?? previous, unresolved)) return { kind: 'stale' };
    return { kind: 'failed', error };
  }
}

export function describeCodexCreditOverageError(
  t: TFunction,
  error: CodexCreditOverageError
): string {
  switch (error.code) {
    case 'missing_auth_index':
    case 'missing_account_id':
      return t('codex_quota.credit_overage_error_unavailable');
    case 'unconfirmed':
    case 'opposite_pending':
      return t('codex_quota.credit_overage_unconfirmed');
    case 'failed':
      return t('codex_quota.credit_overage_error_failed');
    default:
      return t(`codex_quota.credit_overage_error_${error.code}`);
  }
}

/** What the dialog shows, captured when it opens so a refresh cannot swap the account. */
export interface CodexCreditOverageContext {
  key: string;
  accountId: string;
  connectionRevision: number;
  cacheGeneration: number;
  accountLabel: string;
  creditBalance: string | null;
  creditsUnlimited: boolean;
}

export function buildCodexCreditOverageContext(
  file: AuthFileItem,
  quota: CodexQuotaState | undefined
): CodexCreditOverageContext | null {
  const key = codexCreditOverageKey(file);
  const accountId = resolveCodexChatgptAccountId(file);
  if (!key || !accountId) return null;
  return {
    key,
    accountId,
    connectionRevision: apiClient.getConnectionRevision(),
    cacheGeneration: useQuotaStore.getState().cacheGeneration,
    accountLabel: file.email?.trim() || file.name,
    creditBalance: quota?.creditBalance ?? null,
    creditsUnlimited: quota?.creditsUnlimited === true,
  };
}

export function isCodexCreditOverageContextCurrent(
  file: AuthFileItem,
  context: CodexCreditOverageContext
): boolean {
  return (
    codexCreditOverageKey(file) === context.key &&
    apiClient.getConnectionRevision() === context.connectionRevision &&
    useQuotaStore.getState().cacheGeneration === context.cacheGeneration
  );
}
