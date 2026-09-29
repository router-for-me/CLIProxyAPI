import { describe, expect, test } from 'bun:test';
import { createProviderRecentRequestsCacheController } from '../src/components/providers/hooks/useProviderRecentRequests';

describe('provider recent request cache isolation', () => {
  test('creates a fresh cache when the backend or management key changes', () => {
    const controller = createProviderRecentRequestsCacheController();
    const serverA = controller.forScope('https://server-a.example', 'key-a');
    serverA.cachedUsageByProvider = new Map([['provider-a', new Map()]]);
    serverA.cachedAt = Date.now();
    serverA.inFlightRequest = Promise.resolve(serverA.cachedUsageByProvider);

    const serverB = controller.forScope('https://server-b.example', 'key-b');

    expect(serverB).not.toBe(serverA);
    expect(serverB.cachedUsageByProvider.size).toBe(0);
    expect(serverB.cachedAt).toBe(0);
    expect(serverB.inFlightRequest).toBeNull();

    serverA.cachedUsageByProvider = new Map([['late-provider-a', new Map()]]);
    expect(controller.forScope('https://server-b.example', 'key-b')).toBe(serverB);
    expect(serverB.cachedUsageByProvider.size).toBe(0);
  });

  test('reuses the cache only within the same connection scope', () => {
    const controller = createProviderRecentRequestsCacheController();
    const first = controller.forScope('https://server.example', 'key-a');

    expect(controller.forScope('https://server.example', 'key-a')).toBe(first);
    expect(controller.forScope('https://server.example', 'key-b')).not.toBe(first);
  });
});
