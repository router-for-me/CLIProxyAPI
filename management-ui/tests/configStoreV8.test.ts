import { afterEach, describe, expect, test, spyOn } from 'bun:test';
import { configApi } from '../src/services/api/config';
import { useConfigStore } from '../src/stores/useConfigStore';
import type { Config } from '../src/types';

const originalGetConfig = configApi.getConfig;
afterEach(() => {
  configApi.getConfig = originalGetConfig;
  useConfigStore.getState().clearCache();
});

describe('v8 configuration cache', () => {
  test('optimistic UI updates do not add legacy nodes to the server document', async () => {
    const raw = {
      'config-version': 8,
      observability: { logs: { 'request-log': false } },
      'api-keys': { codex: [{ name: 'group', keys: [{ 'api-key': 'fixture' }] }] },
    };
    spyOn(configApi, 'getConfig').mockResolvedValue({ raw, requestLog: false });
    await useConfigStore.getState().fetchConfig();
    useConfigStore.getState().updateConfigValue('request-log', true);
    expect(useConfigStore.getState().config?.requestLog).toBe(true);
    expect(useConfigStore.getState().config?.raw).toEqual(raw);
    expect(useConfigStore.getState().isCacheValid()).toBe(false);
  });

  test('an old connection cannot publish over a new v8 document', async () => {
    let resolveOld!: (value: Config) => void;
    const oldRequest = new Promise<Config>((resolve) => {
      resolveOld = resolve;
    });
    const current = { raw: { 'config-version': 8, access: { 'api-keys': ['new-fixture'] } } };
    const mock = spyOn(configApi, 'getConfig')
      .mockReturnValueOnce(oldRequest)
      .mockResolvedValueOnce(current);
    const old = useConfigStore.getState().fetchConfig();
    useConfigStore.getState().clearCache();
    await useConfigStore.getState().fetchConfig();
    resolveOld({ raw: { access: { 'api-keys': ['old-fixture'] } } });
    await old;
    expect(mock).toHaveBeenCalledTimes(2);
    expect(useConfigStore.getState().config).toEqual(current);
  });
});
