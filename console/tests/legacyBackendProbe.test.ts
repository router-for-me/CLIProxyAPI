import { afterAll, afterEach, beforeAll, describe, expect, spyOn, test } from 'bun:test';
import axios from 'axios';
import { apiClient } from '@/services/api/client';
import { LegacyBackendError } from '@/services/api/legacyBackendProbe';
import { useAuthStore } from '@/stores/useAuthStore';
import { useConfigStore } from '@/stores/useConfigStore';

const spies: Array<{ mockRestore(): void }> = [];
const originalFetchConfig = useConfigStore.getState().fetchConfig;
const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
const memory = new Map<string, string>();
beforeAll(() => {
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => memory.get(key) ?? null,
      setItem: (key: string, value: string) => memory.set(key, value),
      removeItem: (key: string) => memory.delete(key),
    },
  });
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: { location: { host: 'proxy.invalid' } },
  });
});
afterAll(() => {
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage);
  else Reflect.deleteProperty(globalThis, 'localStorage');
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow);
  else Reflect.deleteProperty(globalThis, 'window');
});
afterEach(() => {
  spies.splice(0).forEach((spy) => spy.mockRestore());
  // Zustand replaces its state object; mockRestore alone can leave the mocked action in new states.
  useConfigStore.setState({ fetchConfig: originalFetchConfig });
  apiClient.setConfig({ apiBase: '', managementKey: '' });
  useAuthStore.setState({ isAuthenticated: false, connectionStatus: 'disconnected' });
});

const credentials = {
  apiBase: 'https://proxy.invalid/gateway',
  managementKey: 'fixture-only',
  rememberPassword: false,
};

describe('legacy backend login diagnostic', () => {
  test('confirms v0 config after v8 404 without logging in', async () => {
    const v8Error = Object.assign(new Error('not found'), { status: 404 });
    spies.push(spyOn(useConfigStore.getState(), 'fetchConfig').mockRejectedValue(v8Error));
    const probe = spyOn(axios, 'get').mockResolvedValue({ data: { 'api-keys': [] } });
    spies.push(probe);

    await expect(useAuthStore.getState().login(credentials)).rejects.toBeInstanceOf(
      LegacyBackendError
    );
    expect(probe).toHaveBeenCalledTimes(1);
    expect(probe).toHaveBeenCalledWith(
      'https://proxy.invalid/gateway/v0/management/config',
      expect.objectContaining({ headers: { Authorization: 'Bearer fixture-only' }, timeout: 3000 })
    );
    expect(useAuthStore.getState().isAuthenticated).toBe(false);
  });

  test('does not diagnose auth failures as old backend', async () => {
    const error = Object.assign(new Error('unauthorized'), { status: 401 });
    spies.push(spyOn(useConfigStore.getState(), 'fetchConfig').mockRejectedValue(error));
    const probe = spyOn(axios, 'get');
    spies.push(probe);
    await expect(useAuthStore.getState().login(credentials)).rejects.toBe(error);
    expect(probe).not.toHaveBeenCalled();
  });

  test('keeps the original 404 when the v0 request fails or returns non-config content', async () => {
    const error = Object.assign(new Error('not found'), { status: 404 });
    spies.push(spyOn(useConfigStore.getState(), 'fetchConfig').mockRejectedValue(error));
    const probe = spyOn(axios, 'get').mockResolvedValueOnce({ data: '<html>not a config</html>' });
    spies.push(probe);
    await expect(useAuthStore.getState().login(credentials)).rejects.toBe(error);
    probe.mockRejectedValueOnce(new Error('network unavailable'));
    await expect(useAuthStore.getState().login(credentials)).rejects.toBe(error);
  });

  test('does not publish an obsolete diagnostic after a connection switch', async () => {
    const error = Object.assign(new Error('not found'), { status: 404 });
    spies.push(spyOn(useConfigStore.getState(), 'fetchConfig').mockRejectedValue(error));
    const probe = spyOn(axios, 'get').mockImplementation(async () => {
      apiClient.setConfig({ apiBase: 'https://new.invalid', managementKey: 'other' });
      return { data: {} };
    });
    spies.push(probe);
    await expect(useAuthStore.getState().login(credentials)).rejects.toBe(error);
  });
});
