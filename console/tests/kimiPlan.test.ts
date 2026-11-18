import { afterEach, describe, expect, spyOn, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import { KIMI_CONFIG } from '@/features/quota/providers/kimi/data';
import { KimiQuotaBody } from '@/features/quota/providers/kimi/KimiQuotaBody';
import { bindQuotaClasses, QUOTA_CLASS_KEYS } from '@/features/quota/types';
import { apiCallApi, authFilesApi } from '@/services/api';
import { fetchKimiPlanName, parseKimiPlanName } from '@/services/api/kimiQuota';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { KIMI_USAGE_URL } from '@/utils/quota';

const file = { name: 'kimi-fixture.json', provider: 'kimi', authIndex: 'fixture-index' };
const usage = { usage: { used: 25, limit: 100 } };
const profile = { user_id: 'fixture-user', user_level: 30, user_level_name: 'Vivace' };
const response = (body: unknown, statusCode = 200) => ({
  body,
  statusCode,
  header: {},
  bodyText: '',
});
const classes = bindQuotaClasses(Object.fromEntries(QUOTA_CLASS_KEYS.map((key) => [key, key])));
afterEach(() => useQuotaStore.getState().clearQuotaCache());

describe('Kimi plan profile', () => {
  test('uses the official name without inferring a numeric tier', () => {
    expect(parseKimiPlanName(profile)).toBe('Vivace');
    expect(parseKimiPlanName(JSON.stringify(profile))).toBe('Vivace');
    expect(parseKimiPlanName({ ...profile, user_level_name: ' Future Plan ' })).toBe('Future Plan');
    expect(parseKimiPlanName({ user_id: 'fixture-user', user_level: 30 })).toBeNull();
  });

  test.each([
    null,
    [[]],
    'invalid json',
    {},
    { user_level_name: 'Vivace' },
    { ...profile, user_id: '' },
    { ...profile, user_level_name: 30 },
    { ...profile, user_level_name: ' ' },
  ])('rejects incomplete profiles: %j', (payload) => {
    expect(parseKimiPlanName(payload)).toBeNull();
  });

  test('never forwards credentials to an arbitrary profile host', async () => {
    const request = spyOn(apiCallApi, 'request');
    try {
      expect(await fetchKimiPlanName('fixture-index', 'https://example.invalid/usages')).toBeNull();
      expect(request).not.toHaveBeenCalled();
    } finally {
      request.mockRestore();
    }
  });

  test('stores the official plan and quota, not the full account profile', async () => {
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue('{}');
    const request = spyOn(apiCallApi, 'request')
      .mockResolvedValueOnce(response(usage))
      .mockResolvedValueOnce(response({ ...profile, email: 'private@example.invalid' }));
    try {
      const state = KIMI_CONFIG.buildSuccessState(await KIMI_CONFIG.fetchQuota(file, i18n.t));
      expect(state.planName).toBe('Vivace');
      expect(state.rows).toHaveLength(1);
      expect(JSON.stringify(state)).not.toContain('private@example.invalid');
      expect(request.mock.calls[1][0]).toEqual({
        authIndex: file.authIndex,
        method: 'GET',
        url: KIMI_USAGE_URL.replace('/usages', '/me'),
        header: { Authorization: 'Bearer $TOKEN$', Accept: 'application/json' },
      });
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });

  test.each([401, 403, 404, 500, 'network', 'malformed'])(
    'preserves quota when profile fails: %s',
    async (failure) => {
      const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue('{}');
      const request = spyOn(apiCallApi, 'request').mockImplementation(async ({ url }) => {
        if (url.endsWith('/usages')) return response(usage);
        if (failure === 'network') throw new Error('network unavailable');
        return response({}, typeof failure === 'number' ? failure : 200);
      });
      try {
        const state = KIMI_CONFIG.buildSuccessState(await KIMI_CONFIG.fetchQuota(file, i18n.t));
        expect(state).toMatchObject({ status: 'success', planName: null });
        expect(state.rows).toHaveLength(1);
      } finally {
        download.mockRestore();
        request.mockRestore();
      }
    }
  );

  test.each(['session', 'file'])('rejects %s invalidation during profile lookup', async (scope) => {
    const download = spyOn(authFilesApi, 'downloadText').mockResolvedValue('{}');
    const request = spyOn(apiCallApi, 'request').mockImplementation(async ({ url }) => {
      if (url.endsWith('/usages')) return response(usage);
      useQuotaStore.getState().clearQuotaCache(scope === 'file' ? [file.name] : undefined);
      throw new Error('profile request interrupted');
    });
    try {
      await expect(KIMI_CONFIG.fetchQuota(file, i18n.t)).rejects.toThrow(
        i18n.t('kimi_quota.stale_request')
      );
    } finally {
      download.mockRestore();
      request.mockRestore();
    }
  });

  test('renders the plan even without quota rows, and escapes server text', () => {
    const markup = renderToStaticMarkup(
      createElement(KimiQuotaBody, {
        quota: { status: 'success', rows: [], planName: '<Vivace>' },
        classes,
      })
    );
    expect(markup).toContain(i18n.t('kimi_quota.plan_label'));
    expect(markup).toContain('&lt;Vivace&gt;');
    expect(markup).toContain(i18n.t('kimi_quota.empty_data'));
  });

  test.each([
    ['Allegro', 'elitePlanValue'],
    ['Allegretto', 'premiumPlanValue'],
    ['Moderato', 'codexPlanValue'],
    ['Andante', 'codexPlanValue'],
  ])('renders %s with the matching plan style', (planName, className) => {
    const markup = renderToStaticMarkup(
      createElement(KimiQuotaBody, {
        quota: { status: 'success', rows: [], planName },
        classes,
      })
    );
    expect(markup).toContain(`<span class="${className}">${planName}</span>`);
  });

  test('shows unknown rather than guessing a free or standard plan', () => {
    const markup = renderToStaticMarkup(
      createElement(KimiQuotaBody, {
        quota: { status: 'success', rows: [], planName: null },
        classes,
      })
    );
    expect(markup).toContain(i18n.t('kimi_quota.plan_unknown'));
  });
});
