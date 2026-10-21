import { afterEach, beforeEach, describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { normalizeClaudeUsageSnapshot } from '@/services/api/claudeUsage';
import { apiCallApi, getApiCallErrorMessage, type ApiCallResult } from '@/services/api/apiCall';
import { apiClient } from '@/services/api/client';
import { CLAUDE_CONFIG, hasClaudeUsageData } from '@/features/quota/providers/claude/data';
import { ClaudeQuotaBody } from '@/features/quota/providers/claude/ClaudeQuotaBody';
import { buildTimelineLane } from '@/features/quota/quotaTimelineModel';
import { nextRecoveryMs } from '@/features/quota/resetSchedule';
import { QUOTA_CLASS_KEYS, bindQuotaClasses } from '@/features/quota/types';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from '@/stores/useQuotaStore';
import {
  CLAUDE_PROFILE_URL,
  CLAUDE_USAGE_URL,
  formatQuotaResetTime,
  getStatusFromError,
} from '@/utils/quota';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import type { ClaudeQuotaState } from '@/types';
import fixture from './fixtures/claude-oauth-usage.json';

const translations = i18n.cloneInstance({ lng: 'en' });
const observedAt = '2026-10-10T00:00:00Z';
const file = { name: 'claude-fixture.json', type: 'claude', authIndex: 'synthetic-index' };
const classes = bindQuotaClasses(
  Object.fromEntries(QUOTA_CLASS_KEYS.map((key) => [key, key])),
  'claude-usage-test'
);
const result = (statusCode: number, body: unknown): ApiCallResult => ({
  statusCode,
  header: {},
  body,
  bodyText: JSON.stringify(body),
});
const snapshot = normalizeClaudeUsageSnapshot(fixture)!;
const quota: ClaudeQuotaState = {
  status: 'success',
  windows: [],
  extraUsage: snapshot.extraUsage,
  dollarWindows: snapshot.dollarWindows,
  observedAt,
};
const render = (state: ClaudeQuotaState) =>
  renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n: translations },
      createElement(ClaudeQuotaBody, { quota: state, classes })
    )
  );

describe('Claude OAuth usage API normalization', () => {
  test('preserves every extra-usage field and discovers independent dollar balances', () => {
    const normalized = normalizeClaudeUsageSnapshot(fixture)!;
    expect(normalized.fiveHour).toEqual({
      utilization: 42.5,
      resetsAt: fixture.five_hour.resets_at,
    });
    expect(normalized.sevenDay?.utilization).toBe(16);
    expect(normalized.extraUsage).toEqual({
      isEnabled: true,
      monthlyLimit: 5000,
      usedCredits: 1250,
      utilization: 25,
      currency: 'USD',
      disabledReason: null,
      userDisabled: false,
      spendLimitReached: false,
    });
    expect(normalized.dollarWindows.map((balance) => balance.key)).toEqual([
      'iguana_necktie',
      'additional_credit_balance',
    ]);
    expect(normalized.dollarWindows[0]).toMatchObject({
      limitDollars: 100,
      usedDollars: 5.043595,
      remainingDollars: 94.956405,
      resetsAt: '2026-11-01T00:00:00Z',
    });
    expect(normalized.dollarWindows[1]).toMatchObject({
      usedDollars: 0,
      remainingDollars: 20,
      utilization: 0,
      resetsAt: null,
    });
    expect(normalized).not.toHaveProperty('observedAt');
  });

  test('retains null, false, and zero without fabricating values', () => {
    const normalized = normalizeClaudeUsageSnapshot({
      five_hour: { utilization: null, resets_at: null },
      seven_day: null,
      extra_usage: {
        is_enabled: false,
        monthly_limit: 0,
        used_credits: 0,
        utilization: null,
        currency: 'EUR',
        disabled_reason: 'user_disabled',
        user_disabled: true,
        spend_limit_reached: false,
      },
      future_upstream_key: {
        limit_dollars: null,
        used_dollars: 0,
        remaining_dollars: null,
        utilization: null,
        resets_at: null,
      },
    })!;
    expect(normalized.fiveHour).toEqual({ utilization: null, resetsAt: null });
    expect(normalized.sevenDay).toBeNull();
    expect(normalized.extraUsage).toMatchObject({
      isEnabled: false,
      monthlyLimit: 0,
      usedCredits: 0,
      currency: 'EUR',
      disabledReason: 'user_disabled',
      userDisabled: true,
      spendLimitReached: false,
    });
    expect(normalized.dollarWindows[0]).toEqual({
      key: 'future_upstream_key',
      limitDollars: null,
      usedDollars: 0,
      remainingDollars: null,
      utilization: null,
      resetsAt: null,
    });
  });

  test('ignores incomplete balances and unrelated payloads', () => {
    expect(normalizeClaudeUsageSnapshot({ error: 'failed' })).toBeNull();
    expect(normalizeClaudeUsageSnapshot(null)).toBeNull();
    expect(normalizeClaudeUsageSnapshot([])).toBeNull();
    expect(
      normalizeClaudeUsageSnapshot({ five_hour: null, partial: { used_dollars: 5 } })
    ).toMatchObject({ fiveHour: null, dollarWindows: [] });
  });

  test('keeps every complete top-level dollar window even when its key is dollar_windows', () => {
    const normalized = normalizeClaudeUsageSnapshot({
      ...fixture,
      dollar_windows: { ...fixture.additional_credit_balance, remaining_dollars: 15 },
    })!;
    expect(normalized.dollarWindows.map((balance) => balance.key)).toEqual([
      'iguana_necktie',
      'additional_credit_balance',
      'dollar_windows',
    ]);
    expect(normalized.dollarWindows[2].remainingDollars).toBe(15);
  });

  test('uses only the existing authenticated raw API-call response contract', async () => {
    const body = { error: { message: 'usage unavailable' } };
    const post = spyOn(apiClient, 'post').mockResolvedValue({
      status_code: 429,
      header: {},
      body: JSON.stringify(body),
    });
    try {
      const request = { authIndex: file.authIndex, method: 'GET', url: CLAUDE_USAGE_URL };
      const response = await apiCallApi.request(request);
      expect(post).toHaveBeenCalledWith('/requests/api-call', request, undefined);
      expect(response).toEqual(result(429, body));
      expect(getApiCallErrorMessage(response)).toBe('429 usage unavailable');
    } finally {
      post.mockRestore();
    }
  });
});

describe('Claude quota usage reads', () => {
  const originalRequest = apiCallApi.request;
  afterEach(() => {
    apiCallApi.request = originalRequest;
  });

  test('uses existing quota reads and separates dollar balances from plan windows', async () => {
    const requests: Parameters<typeof apiCallApi.request>[0][] = [];
    apiCallApi.request = async (request) => {
      requests.push(request);
      return request.url === CLAUDE_USAGE_URL
        ? result(200, fixture)
        : result(200, { account: { has_claude_max: true } });
    };
    const data = await CLAUDE_CONFIG.fetchQuota(file, translations.t);
    expect(requests.map((request) => request.url)).toEqual([CLAUDE_USAGE_URL, CLAUDE_PROFILE_URL]);
    expect(requests[0]).toMatchObject({
      method: 'GET',
      authIndex: file.authIndex,
      header: {
        Authorization: 'Bearer $TOKEN$',
        Accept: 'application/json',
        'anthropic-beta': 'oauth-2025-04-20',
      },
    });
    expect(data.windows.map((window) => window.id)).toEqual([
      'five-hour',
      'seven-day',
      'seven-day-sonnet',
    ]);
    expect(data.dollarWindows).toEqual(snapshot.dollarWindows);
    expect(data.extraUsage?.usedCredits).toBe(1250);
    expect(data.planType).toBe('plan_max');
    expect(Number.isFinite(Date.parse(data.observedAt))).toBe(true);
    expect(CLAUDE_CONFIG.buildSuccessState(data)).toMatchObject({
      status: 'success',
      stale: false,
    });
  });

  test('keeps dollar-balance recovery after the fixture plan windows have expired', async () => {
    apiCallApi.request = async (request) =>
      request.url === CLAUDE_USAGE_URL ? result(200, fixture) : result(200, null);
    const state = CLAUDE_CONFIG.buildSuccessState(
      await CLAUDE_CONFIG.fetchQuota(file, translations.t)
    );
    expect(state.windows.map((window) => window.id)).not.toContain('cloud-session-credits');
    expect(nextRecoveryMs('claude', state, Date.parse('2026-10-31T00:00:00Z'))).toBe(
      Date.parse('2026-11-01T00:00:00Z')
    );
  });

  test('rejects every upstream failure even when its body contains usage values', async () => {
    for (const status of [0, 199, 302, 401, 403, 429, 500]) {
      apiCallApi.request = async (request) =>
        request.url === CLAUDE_USAGE_URL
          ? result(status, fixture)
          : result(200, { account: { has_claude_max: true } });
      try {
        await CLAUDE_CONFIG.fetchQuota(file, translations.t);
        throw new Error('A failed read must not reach the success notification path');
      } catch (error) {
        expect(getStatusFromError(error)).toBe(status);
      }
    }
  });

  test('does not fabricate a snapshot when a read fails without cached data', async () => {
    apiCallApi.request = async () => result(401, { error: 'usage unavailable' });
    await expect(CLAUDE_CONFIG.fetchQuota(file, translations.t)).rejects.toThrow(
      '401 usage unavailable'
    );
  });

  test('preserves legacy Fable-only payloads and partial credit pools', async () => {
    for (const window of [
      { utilization: 41, resets_at: fixture.seven_day.resets_at },
      { utilization: 5, remaining_dollars: 95, resets_at: fixture.seven_day.resets_at },
    ]) {
      apiCallApi.request = async (request) =>
        request.url === CLAUDE_USAGE_URL
          ? result(200, { iguana_necktie: window })
          : result(503, null);
      const data = await CLAUDE_CONFIG.fetchQuota(file, translations.t);
      expect(data.windows).toHaveLength(1);
      expect(data.windows[0].id).toBe(
        'remaining_dollars' in window ? 'cloud-session-credits' : 'seven-day-fable'
      );
      expect(data.dollarWindows).toEqual([]);
    }
  });

  test('rejects malformed successful responses rather than erasing the last-good values', async () => {
    for (const body of [null, '', 'not-json', [], { error: 'no usage' }]) {
      apiCallApi.request = async () => result(200, body);
      await expect(CLAUDE_CONFIG.fetchQuota(file, translations.t)).rejects.toThrow(
        translations.t('claude_quota.empty_windows')
      );
    }
  });

  test.each(['upstream', 'management'] as const)(
    'retains frontend last-good values after a rejected %s read and replaces them on recovery',
    async (failure) => {
      let failing = false;
      const post = spyOn(apiClient, 'post').mockImplementation(async (_path, request) => {
        const usage = (request as { url: string }).url === CLAUDE_USAGE_URL;
        if (usage && failing && failure === 'management') {
          throw Object.assign(new Error('request failed'), { status: 502 });
        }
        return {
          status_code: usage && failing ? 429 : 200,
          header: {},
          body: JSON.stringify(
            !usage
              ? { account: { has_claude_max: true } }
              : failing
                ? { error: { message: 'usage unavailable' } }
                : fixture
          ),
        };
      });
      const store = useQuotaStore.getState();
      const key = getQuotaCacheKey(file);
      store.clearQuotaCache();
      try {
        const good = CLAUDE_CONFIG.buildSuccessState(
          await CLAUDE_CONFIG.fetchQuota(file, translations.t)
        );
        store.setClaudeQuota({ [key]: good });
        failing = true;
        store.setClaudeQuota({ [key]: CLAUDE_CONFIG.buildLoadingState() });
        expect(useQuotaStore.getState().claudeQuota[key].windows).toBe(good.windows);
        let failureHandled = false;
        try {
          await CLAUDE_CONFIG.fetchQuota(file, translations.t);
        } catch (error) {
          failureHandled = true;
          store.setClaudeQuota({
            [key]: CLAUDE_CONFIG.buildErrorState(
              (error as Error).message,
              getStatusFromError(error)
            ),
          });
        }
        expect(failureHandled).toBe(true);
        const retained = useQuotaStore.getState().claudeQuota[key];
        expect(retained.status).toBe('error');
        expect(retained.errorStatus).toBe(failure === 'management' ? 502 : 429);
        expect(retained.stale).toBe(true);
        expect(retained.observedAt).toBe(good.observedAt);
        expect(retained.windows).toBe(good.windows);
        expect(retained.extraUsage).toBe(good.extraUsage);
        expect(retained.dollarWindows).toBe(good.dollarWindows);
        failing = false;
        const recovered = CLAUDE_CONFIG.buildSuccessState(
          await CLAUDE_CONFIG.fetchQuota(file, translations.t)
        );
        store.setClaudeQuota({ [key]: recovered });
        expect(useQuotaStore.getState().claudeQuota[key]).toBe(recovered);
        expect(recovered).toMatchObject({ status: 'success', stale: false });
        expect(recovered.error).toBeUndefined();
      } finally {
        post.mockRestore();
        store.clearQuotaCache();
      }
    }
  );

  test('stores raw API-call data once for cards, page counters, recovery sorting, and timeline', async () => {
    const post = spyOn(apiClient, 'post').mockImplementation(async (_path, request) => ({
      status_code: 200,
      header: {},
      body: JSON.stringify(
        (request as { url: string }).url === CLAUDE_USAGE_URL
          ? fixture
          : { account: { has_claude_max: true } }
      ),
    }));
    const store = useQuotaStore.getState();
    store.clearQuotaCache();
    try {
      expect(CLAUDE_CONFIG.storeSelector(useQuotaStore.getState())[file.name]).toBeUndefined();
      const data = await CLAUDE_CONFIG.fetchQuota(file, translations.t);
      const state = CLAUDE_CONFIG.buildSuccessState(data);
      store.setClaudeQuota({ [getQuotaCacheKey(file)]: state });
      const stored = CLAUDE_CONFIG.storeSelector(useQuotaStore.getState())[file.name];
      expect(stored).toBe(state);
      expect(
        Object.values(useQuotaStore.getState().claudeQuota).filter(
          (entry) => entry.status === 'success'
        )
      ).toHaveLength(1);
      expect(nextRecoveryMs('claude', stored, Date.parse(observedAt))).toBe(
        Date.parse('2026-10-15T09:00:00Z')
      );
      expect(
        buildTimelineLane({
          provider: 'claude',
          name: file.name,
          displayName: file.name,
          quota: stored,
          maxPeriodHours: 14 * 24,
        }).anchorMs
      ).toBe(Date.parse('2026-10-15T09:00:00Z'));
      expect(post).toHaveBeenCalledTimes(2);
      store.setClaudeQuota((previous) => previous);
      expect(CLAUDE_CONFIG.storeSelector(useQuotaStore.getState())[file.name]).toBe(state);
    } finally {
      post.mockRestore();
      store.clearQuotaCache();
    }
  });
});

describe('Claude usage snapshot rendering and retention', () => {
  beforeEach(() => {
    useQuotaStore.getState().clearQuotaCache();
  });

  test('offers read-only refresh for every non-idle auth-file quota, including Claude', () => {
    // This host imports SCSS modules, so cover the action wiring here and
    // verify its interaction with the production build in the browser.
    const source = readFileSync(
      new URL('../src/features/authFiles/components/AuthFileQuotaSection.tsx', import.meta.url),
      'utf8'
    );
    // Idle quota is refreshed via the clickable idle prompt; avoid a duplicate button.
    expect(source).toContain("const showRefreshQuotaAction = quotaStatus !== 'idle';");
    expect(source).toContain('{showRefreshQuotaAction && (');
    expect(source).toContain('<div className={styles.quotaCardActions}>');
    expect(source).toContain('onClick={() => void refreshQuotaForFile()}');
    expect(source).toContain("disabled={!canRefreshQuota || quotaStatus === 'loading'}");
    expect(source).toContain("loading={quotaStatus === 'loading'}");
    expect(source).toContain("{t('auth_files.quota_refresh_single')}");
    expect(source).toContain("if (quota?.status === 'loading' || resettingQuota) return;");
    expect(source).toContain(
      'const canRefreshQuota = !disableControls && !file.disabled && !resettingQuota;'
    );
    expect(source).toContain('{resetQuotaAction}');
    expect(CLAUDE_CONFIG).not.toHaveProperty('resetQuota');
  });

  test('renders enabled extra usage in minor units and each balance in dollars', () => {
    const markup = render(quota);
    expect(markup).toContain('Extra Usage');
    expect(markup).toContain('Enabled');
    expect(markup).toContain('Spent $12.50 / Limit $50.00');
    expect(markup).toContain('25% used');
    expect(markup).toContain('iguana_necktie');
    expect(markup).toContain('$94.96 remaining');
    expect(markup).toContain('Spent $5.04 / Limit $100.00');
    expect(markup).toContain(formatQuotaResetTime(fixture.iguana_necktie.resets_at));
    expect(markup).toContain('additional_credit_balance');
    expect(markup).toContain('$20.00 remaining');
    expect(markup).toContain('Spent $0.00 / Limit $20.00');
    expect(markup).toContain('Not provided');
    expect(markup).not.toContain('No quota data available');
  });

  test('renders disabled extra usage, reasons, and zero amounts', () => {
    const markup = render({
      ...quota,
      extraUsage: {
        ...snapshot.extraUsage!,
        isEnabled: false,
        usedCredits: 0,
        monthlyLimit: 0,
        utilization: 0,
        userDisabled: true,
        spendLimitReached: true,
        disabledReason: 'user_disabled',
      },
    });
    expect(markup).toContain('Disabled');
    expect(markup).toContain('Spent $0.00 / Limit $0.00');
    expect(markup).toContain('Disabled by user');
    expect(markup).toContain('Spending limit reached');
    expect(markup).toContain('Reason: user_disabled');
  });

  test('shows the last observation time when a retained snapshot is stale', () => {
    const markup = render({ ...quota, status: 'error', stale: true });
    expect(markup).toContain('role="status"');
    expect(markup).toContain('Showing last successful check:');
    expect(markup).toContain(new Date(observedAt).toLocaleString('en'));
    expect(markup).toContain('$94.96 remaining');
  });

  test('keeps the last good data during loading and errors, then recovers', () => {
    const store = useQuotaStore.getState();
    store.setClaudeQuota({ [file.name]: quota });
    store.setClaudeQuota({ [file.name]: CLAUDE_CONFIG.buildLoadingState() });
    let retained = useQuotaStore.getState().claudeQuota[file.name];
    expect(retained.status).toBe('loading');
    expect(retained.extraUsage).toEqual(quota.extraUsage);
    expect(retained.dollarWindows).toEqual(quota.dollarWindows);
    store.setClaudeQuota({ [file.name]: CLAUDE_CONFIG.buildErrorState('network failed', 502) });
    retained = useQuotaStore.getState().claudeQuota[file.name];
    expect(retained.status).toBe('error');
    expect(retained.stale).toBe(true);
    expect(retained.observedAt).toBe(observedAt);
    expect(retained.dollarWindows).toEqual(quota.dollarWindows);
    const recovered = { ...quota, observedAt: '2026-10-10T01:00:00Z', stale: false };
    store.setClaudeQuota({ [file.name]: recovered });
    expect(useQuotaStore.getState().claudeQuota[file.name]).toEqual(recovered);
  });

  test('does not retain balances across file replacement or session invalidation', () => {
    const store = useQuotaStore.getState();
    store.setClaudeQuota({ [file.name]: quota });
    const pending = captureQuotaCacheGeneration(file.name);
    store.clearQuotaCache([file.name]);
    expect(
      commitIfQuotaCacheCurrent(pending, () => store.setClaudeQuota({ [file.name]: quota }))
    ).toBe(false);
    store.setClaudeQuota({ [file.name]: CLAUDE_CONFIG.buildErrorState('new account read failed') });
    expect(hasClaudeUsageData(useQuotaStore.getState().claudeQuota[file.name])).toBe(false);
    store.setClaudeQuota({ [file.name]: quota });
    const previousSession = captureQuotaCacheGeneration(file.name);
    store.clearQuotaCache();
    expect(
      commitIfQuotaCacheCurrent(previousSession, () => store.setClaudeQuota({ [file.name]: quota }))
    ).toBe(false);
    expect(useQuotaStore.getState().claudeQuota).toEqual({});
  });
});
