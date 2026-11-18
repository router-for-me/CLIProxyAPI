/**
 * Codex account-level "use credits after the usage limit" setting: request contract,
 * outcome classification, and the account-user state kept in the quota store.
 */

import { afterEach, beforeEach, describe, expect, mock, spyOn, test } from 'bun:test';
import * as apiCallModule from '@/services/api/apiCall';
import type { TFunction } from 'i18next';
import {
  CodexCreditOverageError,
  applyCodexCreditOverage,
  buildCodexCreditOverageRequest,
  buildCodexCreditOverageContext,
  codexCreditOverageKey,
  countCodexCreditOverageKeys,
  describeCodexCreditOverageError,
  formatCodexCreditOverageConfirmedAt,
  parseCodexCreditOverageResponse,
  isCodexCreditOverageContextCurrent,
  resolveCodexCreditOverageState,
  sendCodexCreditOverage,
} from '@/features/quota/providers/codex/creditOverage';
import { apiCallApi, apiClient, type ApiCallResult } from '@/services/api';
import { useQuotaStore } from '@/stores/useQuotaStore';
import type { AuthFileItem } from '@/types';
import { CODEX_ACCOUNT_USER_SETTING_URL, CODEX_CREDIT_OVERAGE_FEATURE } from '@/utils/quota';

const ACCOUNT = 'acct-123';
const KEY = `${ACCOUNT}|email:user@example.com`;
const FEATURE = CODEX_CREDIT_OVERAGE_FEATURE;

const codexFile = (overrides: Partial<AuthFileItem> = {}): AuthFileItem => ({
  name: 'codex-a.json',
  type: 'codex',
  auth_index: 'idx-a',
  email: 'User@Example.com',
  chatgpt_account_id: ACCOUNT,
  ...overrides,
});

const response = (body: unknown, statusCode = 200): ApiCallResult => ({
  statusCode,
  body,
  bodyText: JSON.stringify(body),
  header: {},
});

const errorCode = async (run: () => unknown): Promise<string> => {
  try {
    await run();
  } catch (error) {
    expect(error).toBeInstanceOf(CodexCreditOverageError);
    return (error as CodexCreditOverageError).code;
  }
  return 'resolved';
};

const mocks: Array<{ mockRestore(): void }> = [];
const mockApiCall = () => {
  const request = mock<typeof apiCallApi.request>();
  mocks.push(spyOn(apiCallModule, 'captureApiCallApi').mockReturnValue({ request }));
  return request;
};

/** A pending upstream answer the test releases explicitly. */
const deferredApiCall = () => {
  let release: (result: ApiCallResult) => void = () => {};
  const request = mockApiCall().mockImplementation(
    () =>
      new Promise<ApiCallResult>((resolve) => {
        release = resolve;
      })
  );
  return { request, release: (result: ApiCallResult) => release(result) };
};

const stored = () => useQuotaStore.getState().codexCreditOverage[KEY];

beforeEach(() => {
  useQuotaStore.getState().clearQuotaCache();
  useQuotaStore.getState().clearCodexCreditOverageOperations();
});

afterEach(() => {
  for (const mock of mocks.splice(0)) mock.mockRestore();
});

describe('Codex credit overage request', () => {
  test('PATCHes the account-user setting with the outer token placeholder and no body', () => {
    const request = buildCodexCreditOverageRequest(codexFile(), true);
    const url = new URL(request.url);

    expect(request.method).toBe('PATCH');
    expect(request.authIndex).toBe('idx-a');
    expect(`${url.origin}${url.pathname}`).toBe(CODEX_ACCOUNT_USER_SETTING_URL);
    expect(Object.fromEntries(url.searchParams)).toEqual({ feature: FEATURE, value: 'true' });
    expect(request.header?.Authorization).toBe('Bearer $TOKEN$');
    expect(request.header?.['Chatgpt-Account-Id']).toBe(ACCOUNT);
    expect(request.data).toBeUndefined();
    expect(JSON.stringify(request)).not.toContain('siwc');
  });

  test('sends false explicitly', () => {
    const url = new URL(buildCodexCreditOverageRequest(codexFile(), false).url);
    expect(url.searchParams.get('value')).toBe('false');
  });

  test.each([
    ['missing_auth_index', { auth_index: undefined }],
    ['missing_account_id', { chatgpt_account_id: undefined }],
  ])('rejects %s before any request', async (code, overrides) => {
    expect(await errorCode(() => buildCodexCreditOverageRequest(codexFile(overrides), true))).toBe(
      code
    );
  });
});

describe('Codex credit overage account-user key', () => {
  test('normalizes the email and separates workspace members', () => {
    expect(codexCreditOverageKey(codexFile())).toBe(KEY);
    expect(codexCreditOverageKey(codexFile({ name: 'b.json', email: ' user@EXAMPLE.com ' }))).toBe(
      KEY
    );
    expect(codexCreditOverageKey(codexFile({ email: 'teammate@example.com' }))).not.toBe(KEY);
  });

  test('a credential without an email only shares with itself', () => {
    expect(codexCreditOverageKey(codexFile({ email: undefined }))).toBe(
      `${ACCOUNT}|file:codex-a.json`
    );
  });

  test('requires a ChatGPT account ID', () => {
    expect(codexCreditOverageKey(codexFile({ chatgpt_account_id: undefined }))).toBeNull();
  });

  test('counts the credentials sharing each account user', () => {
    const counts = countCodexCreditOverageKeys([
      codexFile(),
      codexFile({ name: 'b.json', auth_index: 'idx-b' }),
      codexFile({ name: 'c.json', email: 'teammate@example.com' }),
      codexFile({ name: 'd.json', chatgpt_account_id: undefined }),
    ]);
    expect(counts.get(KEY)).toBe(2);
    expect(counts.size).toBe(2);
  });
});

describe('Codex credit overage outcome classification', () => {
  const request = () => buildCodexCreditOverageRequest(codexFile(), true);

  test('accepts only a boolean echo of the setting', () => {
    expect(parseCodexCreditOverageResponse({ [FEATURE]: true })).toBe(true);
    expect(parseCodexCreditOverageResponse({ [FEATURE]: false })).toBe(false);
    expect(parseCodexCreditOverageResponse({ [FEATURE]: 'true' })).toBeNull();
    expect(parseCodexCreditOverageResponse({})).toBeNull();
    expect(parseCodexCreditOverageResponse('{"x":1}')).toBeNull();
    expect(parseCodexCreditOverageResponse(null)).toBeNull();
  });

  test('resolves with the value ChatGPT confirmed', async () => {
    mockApiCall().mockResolvedValue(response({ [FEATURE]: true }));
    await expect(sendCodexCreditOverage(request(), true)).resolves.toBe(true);
  });

  test.each([
    [200, { [FEATURE]: false }, 'unconfirmed'],
    [200, {}, 'unconfirmed'],
    [401, { detail: 'rejected_by_access_enforcement' }, 'unauthorized'],
    [403, {}, 'forbidden'],
    [404, {}, 'unsupported'],
    [405, { detail: 'Method Not Allowed' }, 'unsupported'],
    [429, {}, 'rate_limited'],
    [400, {}, 'failed'],
    [500, {}, 'unconfirmed'],
    [503, {}, 'unconfirmed'],
  ])('maps upstream HTTP %d %j to %s', async (status, body, code) => {
    mockApiCall().mockResolvedValue(response(body, status));
    expect(await errorCode(() => sendCodexCreditOverage(request(), true))).toBe(code);
  });

  test.each([
    // The management API refused before forwarding, e.g. the token could not be resolved.
    [400, 'failed'],
    // The upstream call failed in transit and may still have reached ChatGPT.
    [502, 'unconfirmed'],
    // Network failure or client timeout.
    [undefined, 'unconfirmed'],
  ])('maps a thrown management error with status %p to %s', async (status, code) => {
    const error = Object.assign(new Error('boom'), status === undefined ? {} : { status });
    mockApiCall().mockRejectedValue(error);
    expect(await errorCode(() => sendCodexCreditOverage(request(), true))).toBe(code);
  });
});

describe('applyCodexCreditOverage', () => {
  test('records the confirmed value under the account-user key', async () => {
    const request = mockApiCall().mockResolvedValue(response({ [FEATURE]: true }));

    expect(await applyCodexCreditOverage(codexFile(), true)).toEqual({
      kind: 'applied',
      value: true,
    });
    expect(request).toHaveBeenCalledTimes(1);
    expect(stored()).toMatchObject({ status: 'known', value: true });
    // Another credential of the same account user reads the same entry.
    expect(codexCreditOverageKey(codexFile({ name: 'b.json', auth_index: 'idx-b' }))).toBe(KEY);
  });

  test('a definite failure restores what was known before', async () => {
    const before = { status: 'known' as const, value: false, at: 1 };
    useQuotaStore.getState().setCodexCreditOverage(KEY, before);
    mockApiCall().mockResolvedValue(response({}, 403));

    const outcome = await applyCodexCreditOverage(codexFile(), true);

    expect(outcome.kind === 'failed' && outcome.error.code).toBe('forbidden');
    expect(stored()).toBe(before);
  });

  test('an ambiguous failure marks the account user unconfirmed', async () => {
    mockApiCall().mockRejectedValue(new Error('timeout of 30000ms exceeded'));

    const outcome = await applyCodexCreditOverage(codexFile(), true);

    expect(outcome.kind === 'failed' && outcome.error.code).toBe('unconfirmed');
    expect(stored()).toMatchObject({ status: 'unconfirmed', target: true });
  });

  test('allows one write per account user at a time', async () => {
    const { request, release } = deferredApiCall();

    const first = applyCodexCreditOverage(codexFile(), true);
    expect(stored()).toEqual({ status: 'writing', target: true });
    const sibling = codexFile({ name: 'b.json', auth_index: 'idx-b' });
    expect(await applyCodexCreditOverage(sibling, false)).toEqual({ kind: 'busy' });
    expect(request).toHaveBeenCalledTimes(1);

    release(response({ [FEATURE]: true }));
    expect(await first).toEqual({ kind: 'applied', value: true });
  });

  test('shares the account mutex when both credentials lack an email', async () => {
    const { request, release } = deferredApiCall();
    const first = applyCodexCreditOverage(codexFile({ email: undefined }), true);
    const other = codexFile({ name: 'copy.json', auth_index: 'idx-copy', email: undefined });

    expect(await applyCodexCreditOverage(other, false)).toEqual({ kind: 'busy' });
    expect(request).toHaveBeenCalledTimes(1);
    release(response({ [FEATURE]: true }));
    await first;
  });

  test('does not let a missing email bypass a known user account mutex', async () => {
    const { request, release } = deferredApiCall();
    const first = applyCodexCreditOverage(codexFile(), true);
    expect(
      await applyCodexCreditOverage(codexFile({ name: 'copy.json', email: undefined }), false)
    ).toEqual({ kind: 'busy' });
    expect(request).toHaveBeenCalledTimes(1);
    release(response({ [FEATURE]: true }));
    await first;
  });

  test('a different account can write while the first account is pending', async () => {
    let release: (result: ApiCallResult) => void = () => {};
    const request = mockApiCall()
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            release = resolve;
          })
      )
      .mockResolvedValueOnce(response({ [FEATURE]: false }));
    const first = applyCodexCreditOverage(codexFile(), true);
    expect(
      await applyCodexCreditOverage(codexFile({ chatgpt_account_id: 'acct-other' }), false)
    ).toEqual({ kind: 'applied', value: false });
    expect(request).toHaveBeenCalledTimes(2);
    release(response({ [FEATURE]: true }));
    await first;
  });

  test('expires sibling snapshots when their account user cannot be matched reliably', async () => {
    useQuotaStore.getState().setCodexCreditOverage(KEY, { status: 'known', value: false, at: 1 });
    const other = codexFile({ name: 'copy.json', email: undefined });
    mockApiCall().mockResolvedValue(response({ [FEATURE]: true }));
    await applyCodexCreditOverage(other, true);
    expect(stored()).toBeUndefined();
    expect(
      useQuotaStore.getState().codexCreditOverage[codexCreditOverageKey(other)!]
    ).toMatchObject({
      status: 'known',
      value: true,
    });
  });

  test.each([true, false])(
    'blocks the opposite choice after an ambiguous %p write',
    async (target) => {
      const request = mockApiCall().mockRejectedValue(new Error('timeout'));
      await applyCodexCreditOverage(codexFile(), target);
      const opposite = await applyCodexCreditOverage(codexFile(), !target);

      expect(opposite.kind === 'failed' && opposite.error.code).toBe('opposite_pending');
      expect(request).toHaveBeenCalledTimes(1);
      expect(stored()).toMatchObject({ status: 'unconfirmed', target });
    }
  );

  test('keeps the unresolved guard after a same-value retry returns 200', async () => {
    const request = mockApiCall()
      .mockRejectedValueOnce(new Error('timeout'))
      .mockResolvedValueOnce(response({ [FEATURE]: true }));
    await applyCodexCreditOverage(codexFile(), true);
    expect(await applyCodexCreditOverage(codexFile(), true)).toEqual({ kind: 'unconfirmed' });
    const opposite = await applyCodexCreditOverage(codexFile(), false);

    expect(opposite.kind === 'failed' && opposite.error.code).toBe('opposite_pending');
    expect(request).toHaveBeenCalledTimes(2);
    expect(stored()).toMatchObject({ status: 'unconfirmed', target: true });
  });

  test('a definite retry refusal cannot settle the earlier ambiguous write', async () => {
    mockApiCall()
      .mockRejectedValueOnce(new Error('timeout'))
      .mockResolvedValueOnce(response({}, 403));
    await applyCodexCreditOverage(codexFile(), true);
    await applyCodexCreditOverage(codexFile(), true);
    expect(stored()).toMatchObject({ status: 'unconfirmed', target: true });
    expect(useQuotaStore.getState().codexCreditOverageOperations[ACCOUNT]).toMatchObject({
      status: 'unconfirmed',
      target: true,
    });
  });

  test('the unresolved account guard survives per-file and full quota invalidation', async () => {
    const request = mockApiCall().mockRejectedValue(new Error('timeout'));
    await applyCodexCreditOverage(codexFile(), true);
    useQuotaStore.getState().clearQuotaCache(['codex-a.json']);
    useQuotaStore.getState().clearQuotaCache();
    const other = codexFile({ name: 'copy.json', email: undefined });
    const outcome = await applyCodexCreditOverage(other, false);
    expect(outcome.kind === 'failed' && outcome.error.code).toBe('opposite_pending');
    expect(request).toHaveBeenCalledTimes(1);
  });

  test('quota invalidation cannot unlock a still pending write', async () => {
    const { request, release } = deferredApiCall();
    const first = applyCodexCreditOverage(codexFile(), true);
    useQuotaStore.getState().clearQuotaCache();
    expect(await applyCodexCreditOverage(codexFile(), false)).toEqual({ kind: 'busy' });
    release(response({ [FEATURE]: true }));
    expect(await first).toEqual({ kind: 'stale' });
    const opposite = await applyCodexCreditOverage(codexFile(), false);
    expect(opposite.kind === 'failed' && opposite.error.code).toBe('opposite_pending');
    expect(request).toHaveBeenCalledTimes(1);
  });

  test('a late old-session answer cannot release a replacement session lock', async () => {
    const releases: Array<(result: ApiCallResult) => void> = [];
    mockApiCall().mockImplementation(
      () =>
        new Promise((resolve) => {
          releases.push(resolve);
        })
    );
    const old = applyCodexCreditOverage(codexFile(), true);
    useQuotaStore.getState().clearQuotaCache();
    useQuotaStore.getState().clearCodexCreditOverageOperations();
    const replacement = applyCodexCreditOverage(codexFile(), false);
    releases[0](response({ [FEATURE]: true }));
    expect(await old).toEqual({ kind: 'stale' });
    expect(useQuotaStore.getState().codexCreditOverageOperations[ACCOUNT]).toMatchObject({
      status: 'writing',
      target: false,
    });
    releases[1](response({ [FEATURE]: false }));
    expect(await replacement).toEqual({ kind: 'applied', value: false });
  });

  test('discards the answer after the session cache is cleared', async () => {
    const { release } = deferredApiCall();

    const pending = applyCodexCreditOverage(codexFile(), true);
    useQuotaStore.getState().clearQuotaCache();
    release(response({ [FEATURE]: true }));

    expect(await pending).toEqual({ kind: 'stale' });
    expect(stored()).toBeUndefined();
  });

  test('drops its writing marker without committing after a connection switch', async () => {
    let revision = 7;
    mocks.push(spyOn(apiClient, 'getConnectionRevision').mockImplementation(() => revision));
    const { release } = deferredApiCall();

    const pending = applyCodexCreditOverage(codexFile(), true);
    revision = 8;
    release(response({ [FEATURE]: true }));

    expect(await pending).toEqual({ kind: 'stale' });
    expect(stored()).toBeUndefined();
  });

  test('missing identity fails without touching the store or the network', async () => {
    const request = mockApiCall();

    const outcome = await applyCodexCreditOverage(codexFile({ auth_index: undefined }), true);

    expect(outcome.kind === 'failed' && outcome.error.code).toBe('missing_auth_index');
    expect(request).not.toHaveBeenCalled();
    expect(useQuotaStore.getState().codexCreditOverage).toEqual({});
  });
});

describe('Codex credit overage session scope', () => {
  test('a dialog cannot carry authorization across a replaced session', () => {
    const context = buildCodexCreditOverageContext(codexFile(), undefined)!;
    expect(isCodexCreditOverageContextCurrent(codexFile(), context)).toBe(true);
    useQuotaStore.getState().clearQuotaCache();
    expect(isCodexCreditOverageContextCurrent(codexFile(), context)).toBe(false);
  });

  test('a dialog cannot act on a replacement account user', () => {
    const context = buildCodexCreditOverageContext(codexFile(), undefined)!;
    expect(
      isCodexCreditOverageContextCurrent(codexFile({ email: 'other@example.com' }), context)
    ).toBe(false);
  });
  test('per-file invalidation keeps account state; a full clear drops it', () => {
    useQuotaStore.getState().setCodexCreditOverage(KEY, { status: 'known', value: true, at: 1 });

    useQuotaStore.getState().clearQuotaCache(['codex-a.json']);
    expect(stored()).toEqual({ status: 'known', value: true, at: 1 });

    useQuotaStore.getState().clearQuotaCache();
    expect(useQuotaStore.getState().codexCreditOverage).toEqual({});
  });
});

describe('Codex credit overage presentation', () => {
  test('maps store entries to display states, defaulting to unknown', () => {
    expect(resolveCodexCreditOverageState(undefined)).toBe('unknown');
    expect(resolveCodexCreditOverageState({ status: 'writing', target: true })).toBe('writing');
    expect(resolveCodexCreditOverageState({ status: 'known', value: true, at: 1 })).toBe('allowed');
    expect(resolveCodexCreditOverageState({ status: 'known', value: false, at: 1 })).toBe(
      'blocked'
    );
    expect(resolveCodexCreditOverageState({ status: 'unconfirmed', target: false, at: 1 })).toBe(
      'unconfirmed'
    );
  });

  test('shows the confirmation time alone on the same day and adds the date across days', () => {
    const confirmed = new Date(2026, 10, 8, 18, 49).getTime();

    const sameDay = formatCodexCreditOverageConfirmedAt(
      confirmed,
      new Date(2026, 10, 8, 23, 0).getTime()
    );
    expect(sameDay).toMatch(/18\D?49/);
    expect(sameDay).not.toMatch(/11\D08|08\D11/);

    const nextDay = formatCodexCreditOverageConfirmedAt(
      confirmed,
      new Date(2026, 10, 9, 0, 5).getTime()
    );
    expect(nextDay).toMatch(/11\D08|08\D11/);
    expect(nextDay).toMatch(/18\D?49/);
  });

  test('describes each failure with a localized message', () => {
    const t = ((key: string, options?: { message?: string }) =>
      options?.message ? `${key}:${options.message}` : key) as unknown as TFunction;
    const describeCode = (code: CodexCreditOverageError['code'], message?: string) =>
      describeCodexCreditOverageError(t, new CodexCreditOverageError(code, message));

    expect(describeCode('missing_auth_index')).toBe('codex_quota.credit_overage_error_unavailable');
    expect(describeCode('missing_account_id')).toBe('codex_quota.credit_overage_error_unavailable');
    expect(describeCode('unconfirmed')).toBe('codex_quota.credit_overage_unconfirmed');
    expect(describeCode('opposite_pending')).toBe('codex_quota.credit_overage_unconfirmed');
    expect(describeCode('failed', 'raw upstream response')).toBe(
      'codex_quota.credit_overage_error_failed'
    );
    for (const code of ['unauthorized', 'forbidden', 'unsupported', 'rate_limited'] as const) {
      expect(describeCode(code)).toBe(`codex_quota.credit_overage_error_${code}`);
    }
  });

  test('every locale carries the dynamic keys with matching interpolation', async () => {
    const dynamicKeys = [
      ...['unknown', 'writing', 'allowed', 'blocked', 'unconfirmed'].map(
        (state) => `credit_overage_state_${state}`
      ),
      ...['unauthorized', 'forbidden', 'unsupported', 'rate_limited', 'failed', 'unavailable'].map(
        (code) => `credit_overage_error_${code}`
      ),
    ];
    const tokens = (value: string) => (value.match(/\{\{[^}]+\}\}/g) ?? []).sort().join('|');
    const readSection = async (locale: string) =>
      (
        (await Bun.file(`src/i18n/locales/${locale}.json`).json()) as {
          codex_quota: Record<string, string>;
        }
      ).codex_quota;

    const english = await readSection('en');
    const ownKeys = Object.keys(english).filter((key) => key.startsWith('credit_overage_'));
    expect(ownKeys.length).toBeGreaterThan(0);
    for (const key of dynamicKeys) expect(ownKeys).toContain(key);

    for (const locale of ['zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
      const section = await readSection(locale);
      for (const key of ownKeys) {
        expect({ locale, key, present: Boolean(section[key]?.trim()) }).toEqual({
          locale,
          key,
          present: true,
        });
        expect({ locale, key, tokens: tokens(section[key]) }).toEqual({
          locale,
          key,
          tokens: tokens(english[key]),
        });
      }
    }
  });
});
