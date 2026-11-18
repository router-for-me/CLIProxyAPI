import { beforeEach, describe, expect, test } from 'bun:test';
import { invalidateAuthFileDerivedCaches } from '../src/features/authFiles/cacheInvalidation';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from '../src/stores/useQuotaStore';

describe('quota cache session isolation', () => {
  beforeEach(() => {
    useQuotaStore.getState().clearQuotaCache();
  });

  test('prevents an earlier connection from committing after the cache is cleared', () => {
    const previousConnection = captureQuotaCacheGeneration();
    let committed = false;

    useQuotaStore.getState().clearQuotaCache();

    expect(
      commitIfQuotaCacheCurrent(previousConnection, () => {
        committed = true;
      })
    ).toBe(false);
    expect(committed).toBe(false);
  });

  test('allows the current connection generation to commit', () => {
    const currentConnection = captureQuotaCacheGeneration();
    let committed = false;

    expect(
      commitIfQuotaCacheCurrent(currentConnection, () => {
        committed = true;
      })
    ).toBe(true);
    expect(committed).toBe(true);
  });

  test('single-file invalidation preserves other credentials and their pending requests', () => {
    const target = { status: 'success' as const, windows: [] };
    const other = { status: 'loading' as const, windows: [] };
    useQuotaStore.getState().setCodexQuota({ 'a.json': target, 'b.json': other });
    useQuotaStore.getState().setClaudeQuota({ 'c.json': { status: 'loading', windows: [] } });
    const claudeCache = useQuotaStore.getState().claudeQuota;
    const targetRequest = captureQuotaCacheGeneration('a.json');
    const otherRequest = captureQuotaCacheGeneration('b.json');
    const batchRequest = captureQuotaCacheGeneration();

    invalidateAuthFileDerivedCaches(() => {}, ['a.json']);

    expect(useQuotaStore.getState().codexQuota).toEqual({ 'b.json': other });
    expect(useQuotaStore.getState().codexQuota['b.json']).toBe(other);
    expect(useQuotaStore.getState().claudeQuota).toBe(claudeCache);
    expect(commitIfQuotaCacheCurrent(targetRequest, () => {})).toBe(false);
    expect(commitIfQuotaCacheCurrent(otherRequest, () => {})).toBe(true);
    expect(commitIfQuotaCacheCurrent(batchRequest, () => {}, 'a.json')).toBe(false);
    expect(commitIfQuotaCacheCurrent(batchRequest, () => {}, 'b.json')).toBe(true);
    expect(commitIfQuotaCacheCurrent(captureQuotaCacheGeneration('a.json'), () => {})).toBe(true);

    useQuotaStore.getState().clearQuotaCache();
    expect(commitIfQuotaCacheCurrent(otherRequest, () => {})).toBe(false);
    expect(commitIfQuotaCacheCurrent(batchRequest, () => {}, 'b.json')).toBe(false);
  });

  test('empty names do nothing; omitted names still invalidate all credentials', () => {
    useQuotaStore.getState().setCodexQuota({ 'a.json': { status: 'loading', windows: [] } });
    const request = captureQuotaCacheGeneration('a.json');
    const previous = useQuotaStore.getState();
    invalidateAuthFileDerivedCaches(() => {}, []);
    expect(useQuotaStore.getState()).toBe(previous);
    invalidateAuthFileDerivedCaches(() => {});
    expect(useQuotaStore.getState().codexQuota).toEqual({});
    expect(commitIfQuotaCacheCurrent(request, () => {})).toBe(false);
  });

  test('clears same-name quota and rejects an in-flight commit after auth mutation', () => {
    const fileName = 'shared-codex.json';
    useQuotaStore.getState().setCodexQuota({
      [fileName]: {
        status: 'success',
        windows: [],
        planType: 'account-a',
      },
    });
    const accountARequest = captureQuotaCacheGeneration(fileName);
    let invalidatedNames: string[] | undefined;

    invalidateAuthFileDerivedCaches(
      (names) => {
        invalidatedNames = names;
      },
      [fileName]
    );

    expect(invalidatedNames).toEqual([fileName]);
    expect(useQuotaStore.getState().codexQuota[fileName]).toBeUndefined();

    let committed = false;
    expect(
      commitIfQuotaCacheCurrent(accountARequest, () => {
        committed = true;
      })
    ).toBe(false);
    expect(committed).toBe(false);
  });
});
