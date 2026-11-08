import { beforeEach, describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import {
  clearSharingDataCache,
  useSharingData,
  writeSharingDataCache,
} from '../src/features/sharing/hooks';
import { apiClient } from '../src/services/api/client';

function Probe({ cacheKey }: { cacheKey?: string }) {
  const { data, loading } = useSharingData<string>(async () => 'fresh', {
    keepPreviousData: false,
    headerRefresh: false,
    cacheKey,
  });
  return createElement('span', null, `${data ?? 'empty'}|${loading ? 'loading' : 'idle'}`);
}

const render = (cacheKey?: string) =>
  renderToStaticMarkup(createElement(Probe, { cacheKey }));

describe('useSharingData result cache', () => {
  beforeEach(() => {
    clearSharingDataCache();
    apiClient.setConfig({ apiBase: 'http://cache.test', managementKey: 'key-a' });
  });

  test('paints a cached result at once while the revalidation is still loading', () => {
    expect(render('page:1')).toContain('empty|loading');
    writeSharingDataCache('page:1', 'cached');
    expect(render('page:1')).toContain('cached|loading');
  });

  test('is keyed exactly: other keys and uncached hooks start empty', () => {
    writeSharingDataCache('page:1', 'cached');
    expect(render('page:2')).toContain('empty|loading');
    expect(render(undefined)).toContain('empty|loading');
  });

  test('a connection change drops every cached result', () => {
    writeSharingDataCache('page:1', 'cached');
    apiClient.setConfig({ apiBase: 'http://cache.test', managementKey: 'key-b' });
    expect(render('page:1')).toContain('empty|loading');
  });

  test('keeps only the most recent entries', () => {
    for (let index = 0; index < 60; index++) writeSharingDataCache(`k:${index}`, index);
    expect(render('k:0')).toContain('empty|loading');
    expect(render('k:59')).toContain('59|loading');
  });
});
