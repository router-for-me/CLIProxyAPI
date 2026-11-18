import { describe, expect, spyOn, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '../src/i18n/index';
import { authFilesApi, normalizeAuthFileRefreshResults } from '../src/services/api/authFiles';
import { apiClient } from '../src/services/api/client';
import { getAuthFileRefreshKey } from '../src/features/authFiles/manualRefresh';
import { AuthFileRefreshResultsContent } from '../src/features/authFiles/components/AuthFileRefreshResults';

const results = [
  { id: 'one.json', success: true },
  { id: 'two.json', success: false, error: 'Refresh rejected' },
];

describe('credential refresh operations', () => {
  test('refreshes all with a bounded extended timeout and preserves partial failures', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({ ok: true, results });
    try {
      expect(await authFilesApi.requestAllManualRefresh()).toEqual(results);
      expect(post).toHaveBeenCalledWith(
        '/credentials/refresh',
        { all: true },
        { timeout: 300_000 }
      );
    } finally {
      post.mockRestore();
    }
  });

  test('single refresh passes auth_index and never returns token metadata', async () => {
    const post = spyOn(apiClient, 'post').mockResolvedValue({
      ok: true,
      auth: { metadata: { access_token: 'fixture-not-a-secret' } },
    });
    try {
      expect(await authFilesApi.requestManualRefresh('same.json', 'index-2')).toBeUndefined();
      expect(post).toHaveBeenCalledWith('/credentials/refresh', {
        name: 'same.json',
        auth_index: 'index-2',
      });
    } finally {
      post.mockRestore();
    }
  });

  test('separates pending refresh identities for all providers, not only Devin', () => {
    expect(getAuthFileRefreshKey({ name: 'same.json', type: 'codex', authIndex: '1' })).not.toBe(
      getAuthFileRefreshKey({ name: 'same.json', type: 'codex', authIndex: '2' })
    );
  });

  test('accepts no eligible credentials and whitelists result fields', () => {
    expect(normalizeAuthFileRefreshResults({ ok: true, results: [] })).toEqual([]);
    expect(
      normalizeAuthFileRefreshResults({
        ok: true,
        results: [{ ...results[0], metadata: { token: 'fixture' }, error: 'irrelevant' }],
      })
    ).toEqual([results[0]]);
  });

  test('does not silently report success for malformed results', () => {
    for (const payload of [
      {},
      { ok: true },
      { ok: true, results: null },
      { ok: false, results: [] },
      { ok: true, results: [{ id: 'one.json', success: 'true' }] },
      { ok: true, results: [{ id: '', success: true }] },
    ]) {
      expect(() => normalizeAuthFileRefreshResults(payload)).toThrow();
    }
  });

  test('propagates transport failures rather than inventing per-item results', async () => {
    const post = spyOn(apiClient, 'post').mockRejectedValue(new Error('timeout'));
    try {
      await expect(authFilesApi.requestAllManualRefresh()).rejects.toThrow('timeout');
    } finally {
      post.mockRestore();
    }
  });
});

describe('refresh result presentation', () => {
  test('shows individual successes and failures even when HTTP succeeded', () => {
    const html = renderToStaticMarkup(createElement(AuthFileRefreshResultsContent, { results }));
    expect(html).toContain(i18n.t('auth_files.refresh_all_summary', { success: 1, failed: 1 }));
    expect(html).toContain('one.json');
    expect(html).toContain('two.json');
    expect(html).toContain('Refresh rejected');
    expect(html).toContain('role="status"');
  });

  test('shows empty eligibility explicitly and escapes error markup', () => {
    const empty = renderToStaticMarkup(
      createElement(AuthFileRefreshResultsContent, { results: [] })
    );
    expect(empty).toContain(i18n.t('auth_files.refresh_all_empty'));
    const html = renderToStaticMarkup(
      createElement(AuthFileRefreshResultsContent, {
        results: [{ id: 'fixture', success: false, error: '<script>fixture</script>' }],
      })
    );
    expect(html).not.toContain('<script>');
    expect(html).toContain('&lt;script&gt;');
  });
});
