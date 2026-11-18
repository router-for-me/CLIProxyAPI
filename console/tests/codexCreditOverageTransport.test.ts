import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import axios, { AxiosError, type AxiosAdapter, type AxiosInstance } from 'axios';
import { apiClient, captureApiCallApi } from '@/services/api';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { applyCodexCreditOverage } from '@/features/quota/providers/codex/creditOverage';
import type { AuthFileItem } from '@/types';

const file: AuthFileItem = {
  name: 'transport-fixture.json',
  type: 'codex',
  auth_index: 'fixture-index',
  email: 'fixture@example.invalid',
  chatgpt_account_id: 'fixture-account',
};
const feature = 'can_consume_personal_credits_chatpass';
const instance = (apiClient as unknown as { instance: AxiosInstance }).instance;
const originalAdapter = axios.defaults.adapter;
const originalInstanceAdapter = instance.defaults.adapter;
const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
let events: string[];

const installAdapter = (adapter: AxiosAdapter) => {
  // Both paths are intercepted, so a transport regression cannot make a real request.
  axios.defaults.adapter = adapter;
  instance.defaults.adapter = adapter;
};

beforeEach(() => {
  events = [];
  Object.defineProperty(globalThis, 'window', {
    configurable: true,
    value: {
      dispatchEvent: (event: Event) => {
        events.push(event.type);
        return true;
      },
    },
  });
  useQuotaStore.getState().clearQuotaCache();
  useQuotaStore.getState().clearCodexCreditOverageOperations();
  apiClient.setConfig({ apiBase: 'https://original.invalid', managementKey: 'original-fixture' });
});

afterEach(() => {
  axios.defaults.adapter = originalAdapter;
  instance.defaults.adapter = originalInstanceAdapter;
  apiClient.setConfig({ apiBase: '', managementKey: '' });
  useQuotaStore.getState().clearQuotaCache();
  useQuotaStore.getState().clearCodexCreditOverageOperations();
  if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow);
  else Reflect.deleteProperty(globalThis, 'window');
});

describe('Codex credit overage connection isolation', () => {
  test('pins the destination, management key and credential before asynchronous dispatch', async () => {
    const sent: Array<{ base: string | undefined; key: unknown; authIndex: unknown }> = [];
    installAdapter(async (config) => {
      sent.push({
        base: config.baseURL,
        key: config.headers.Authorization,
        authIndex: JSON.parse(config.data as string).authIndex,
      });
      expect(config.url).toBe('/requests/api-call');
      return {
        status: 200,
        statusText: 'OK',
        config,
        headers: { 'x-cpa-version': 'old', 'x-cpa-support-plugin': 'true' },
        data: { status_code: 200, body: JSON.stringify({ [feature]: true }) },
      };
    });
    const pending = applyCodexCreditOverage(file, true);
    apiClient.setConfig({ apiBase: 'https://replacement.invalid', managementKey: 'new-fixture' });
    expect(await pending).toEqual({ kind: 'stale' });
    expect(sent).toEqual([
      {
        base: 'https://original.invalid/v8/management',
        key: 'Bearer original-fixture',
        authIndex: 'fixture-index',
      },
    ]);
    expect(events).toEqual([]);
  });

  test.each(['connection', 'session', 'ABA'])(
    'does not emit logout for a 401 after %s replacement',
    async (replacement) => {
      let started: () => void = () => {};
      const dispatched = new Promise<void>((resolve) => {
        started = resolve;
      });
      let refuse: () => void = () => {};
      installAdapter(
        (config) =>
          new Promise((_resolve, reject) => {
            refuse = () =>
              reject(
                new AxiosError('fixture unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
                  status: 401,
                  statusText: 'Unauthorized',
                  headers: {},
                  config,
                  data: {},
                })
              );
            started();
          })
      );
      const pending = applyCodexCreditOverage(file, true);
      await dispatched;
      if (replacement === 'session') {
        useQuotaStore.getState().clearQuotaCache();
      } else {
        apiClient.setConfig({
          apiBase: 'https://replacement.invalid',
          managementKey: 'new-fixture',
        });
        if (replacement === 'ABA') {
          apiClient.setConfig({
            apiBase: 'https://original.invalid',
            managementKey: 'original-fixture',
          });
        }
      }
      refuse();
      expect(await pending).toEqual({ kind: 'stale' });
      expect(events).toEqual([]);
      expect(useQuotaStore.getState().codexCreditOverage).toEqual({});
    }
  );

  test('still emits logout for a current management 401', async () => {
    installAdapter(async (config) => {
      throw new AxiosError('fixture unauthorized', 'ERR_BAD_REQUEST', config, undefined, {
        status: 401,
        statusText: 'Unauthorized',
        headers: {},
        config,
        data: {},
      });
    });
    const outcome = await applyCodexCreditOverage(file, true);
    expect(outcome.kind).toBe('failed');
    expect(events).toEqual(['unauthorized']);
  });

  test('retains current version/plugin events and configured timeout in a captured client', async () => {
    apiClient.setConfig({
      apiBase: 'https://original.invalid',
      managementKey: 'original-fixture',
      timeout: 45000,
    });
    installAdapter(async (config) => {
      expect(config.timeout).toBe(45000);
      return {
        status: 200,
        statusText: 'OK',
        config,
        headers: { 'x-cpa-version': 'fixture-version', 'x-cpa-support-plugin': 'true' },
        data: { status_code: 200, body: JSON.stringify({ [feature]: true }) },
      };
    });
    const result = await captureApiCallApi().request({
      method: 'PATCH',
      url: 'https://chatgpt.com/fixture',
    });
    expect(result.body).toEqual({ [feature]: true });
    expect(events).toEqual(['server-version-update', 'server-plugin-support-update']);
  });
});
