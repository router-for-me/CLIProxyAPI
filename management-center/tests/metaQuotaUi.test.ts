import { afterEach, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { readFileSync } from 'node:fs';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '@/i18n';
import type { AuthFileItem, MetaQuotaState } from '@/types';
import { MetaQuotaBody } from '@/features/quota/providers/meta/MetaQuotaBody';
import { META_CONFIG } from '@/features/quota/providers/meta/data';
import { parseMetaQuotaPayload } from '@/services/api/metaQuota';
import { metaQuotaResponse } from './fixtures/metaQuota';
import { QUOTA_CLASS_KEYS, bindQuotaClasses } from '@/features/quota/types';
import { classifyQuotaFiles, buildTabCounts } from '@/features/quota/logic';
import { QUOTA_PROVIDER_TYPES } from '@/features/authFiles/constants';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from '@/stores/useQuotaStore';

const file: AuthFileItem = { name: 'meta-fixture.json', type: 'meta', authIndex: 'fixture-index' };
const classes = bindQuotaClasses(
  Object.fromEntries(QUOTA_CLASS_KEYS.map((key) => [key, key])),
  'test'
);
const snapshot = (): MetaQuotaState => ({
  status: 'success',
  data: {
    planName: 'Muse fixture plan',
    isSubscriptionActive: true,
    windows: [
      { id: 'window', usedPercent: 0, durationMinutes: 300, resetAt: 4070995200 },
      { id: 'weekly', usedPercent: 1, resetAt: 4071513600 },
    ],
  },
});
afterEach(() => useQuotaStore.getState().clearQuotaCache());

describe('Muse quota UI integration', () => {
  test('provides quota labels in all four locales', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru']) {
      const translations = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      for (const key of [
        'title',
        'empty_title',
        'empty_desc',
        'idle',
        'loading',
        'load_failed',
        'missing_auth_index',
        'missing_file',
        'missing_dca_token',
        'invalid_auth_file',
        'download_failed',
        'stale_request',
        'empty_data',
        'request_failed',
        'invalid_response',
        'plan',
        'active',
        'inactive',
        'window',
        'window_duration',
        'weekly',
        'unknown',
        'remaining',
      ]) {
        expect(translations.meta_quota[key]).toBeTruthy();
      }
    }
  });
  test('registers quota and filters disabled credentials', () => {
    expect(QUOTA_PROVIDER_TYPES.has('meta')).toBe(true);
    expect(META_CONFIG.filterFn(file)).toBe(true);
    expect(META_CONFIG.filterFn({ ...file, disabled: true })).toBe(false);
    const entries = classifyQuotaFiles([file, { ...file, name: 'disabled.json', disabled: true }]);
    expect(entries).toEqual([{ file, type: 'meta' }]);
    expect(buildTabCounts(entries).meta).toBe(1);
  });

  test('converts used percent into remaining meters and displays plan and windows', () => {
    const markup = renderToStaticMarkup(
      createElement(MetaQuotaBody, { quota: snapshot(), classes })
    );
    expect(markup).toContain('Muse fixture plan');
    expect(markup).toContain(i18n.t('meta_quota.active'));
    expect(markup).toContain(i18n.t('meta_quota.window_duration', { minutes: 300 }));
    expect(markup).toContain(i18n.t('meta_quota.weekly'));
    expect(markup.match(/role="meter"/g)).toHaveLength(2);
    expect(markup).toContain('aria-valuenow="100"');
    expect(markup).toContain('aria-valuenow="99"');
    expect(markup).toContain('width:100%');
    expect(markup).toContain('width:99%');
  });

  test('distinguishes unknown and exhausted quotas', () => {
    const quota = snapshot();
    quota.data!.windows[0].usedPercent = null;
    quota.data!.windows[1].usedPercent = 100;
    const markup = renderToStaticMarkup(createElement(MetaQuotaBody, { quota, classes }));
    expect(markup).toContain(i18n.t('meta_quota.unknown'));
    expect(markup.match(/role="meter"/g)).toHaveLength(1);
    expect(markup).toContain('aria-valuenow="0"');
  });

  test('renders the key endpoint response as 98% window and 100% weekly remaining', () => {
    const data = parseMetaQuotaPayload(metaQuotaResponse)!;
    const markup = renderToStaticMarkup(
      createElement(MetaQuotaBody, { quota: META_CONFIG.buildSuccessState(data), classes })
    );
    expect(markup).toContain('Muse Code Everyday Usage');
    expect(markup).toContain('aria-valuenow="98"');
    expect(markup).toContain('aria-valuenow="100"');
    expect(markup).not.toContain(i18n.t('meta_quota.empty_data'));
    expect(markup).not.toContain('fixture@example.invalid');
    expect(markup).not.toContain('LLM|');
  });

  test('missing usage shows unknown windows and a first-request hint, not full meters', () => {
    const data = parseMetaQuotaPayload({ api_key: 'LLM|fixture-only' })!;
    const quota = META_CONFIG.buildSuccessState(data);
    expect(quota.status).toBe('success');
    const markup = renderToStaticMarkup(createElement(MetaQuotaBody, { quota, classes }));
    expect(markup).toContain(i18n.t('meta_quota.empty_data'));
    expect(markup).toContain(i18n.t('meta_quota.window'));
    expect(markup).toContain(i18n.t('meta_quota.weekly'));
    expect(markup).toContain(i18n.t('meta_quota.unknown'));
    expect(markup).not.toContain('role="meter"');
    expect(markup).not.toContain('width:100%');
    expect(markup).not.toContain('LLM|');
  });

  test('invalidates file-scoped and session caches and rejects stale writes', () => {
    const quota = snapshot();
    useQuotaStore.getState().setMetaQuota({ [file.name]: quota, 'other.json': quota });
    const generation = captureQuotaCacheGeneration(file.name);
    useQuotaStore.getState().clearQuotaCache([file.name]);
    expect(useQuotaStore.getState().metaQuota[file.name]).toBeUndefined();
    expect(useQuotaStore.getState().metaQuota['other.json']).toBe(quota);
    expect(commitIfQuotaCacheCurrent(generation, () => {})).toBe(false);
    const otherGeneration = captureQuotaCacheGeneration('other.json');
    useQuotaStore.getState().clearQuotaCache();
    expect(useQuotaStore.getState().metaQuota).toEqual({});
    expect(commitIfQuotaCacheCurrent(otherGeneration, () => {})).toBe(false);
  });
});
