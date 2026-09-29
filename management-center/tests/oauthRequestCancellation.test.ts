import { describe, expect, test } from 'bun:test';
import { AxiosError, type AxiosResponse } from 'axios';
import { apiClient } from '@/services/api/client';
import { createOAuthAttempts } from '@/pages/oauthAttempts';

// Use the real Axios interceptor chain with a deferred transport response.
// Abandoned requests must not emit old server metadata or log out a new session.
describe('OAuth transport cancellation', () => {
  for (const status of [200, 401]) {
    test(`suppresses stale response side effects (${status})`, async () => {
      const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
      const events: string[] = [];
      Object.defineProperty(globalThis, 'window', {
        configurable: true,
        value: { dispatchEvent: (event: Event) => events.push(event.type) },
      });
      let dispatched!: () => void;
      const started = new Promise<void>((resolve) => {
        dispatched = resolve;
      });
      let deliver!: () => void;
      const attempts = createOAuthAttempts({ setTimeout: () => 0, clearTimeout: () => {} });
      const attempt = attempts.begin('devin');
      try {
        const request = apiClient.get('/get-auth-status', {
          signal: attempt.signal,
          adapter: (config) =>
            new Promise<AxiosResponse>((resolve, reject) => {
              deliver = () => {
                const response: AxiosResponse = {
                  config,
                  data: { status: 'ok' },
                  status,
                  statusText: String(status),
                  headers: { 'X-CPA-Version': 'old-server', 'X-CPA-Support-Plugin': 'true' },
                };
                if (status === 401)
                  reject(
                    new AxiosError('Unauthorized', 'ERR_BAD_REQUEST', config, undefined, response)
                  );
                else resolve(response);
              };
              dispatched();
            }),
        });
        await started;
        attempts.invalidateAll();
        const current = attempts.begin('devin');
        deliver();
        await expect(request).rejects.toHaveProperty('code', 'ERR_CANCELED');
        expect(events).toEqual([]);
        expect(current.signal.aborted).toBe(false);
      } finally {
        attempts.invalidateAll();
        if (originalWindow) Object.defineProperty(globalThis, 'window', originalWindow);
        else Reflect.deleteProperty(globalThis, 'window');
      }
    });
  }
});
