import { afterEach, describe, expect, test } from 'bun:test';
import { claudeToResource } from '../src/features/providers/adapters';
import { apiClient } from '../src/services/api/client';
import { providersApi } from '../src/services/api/providers';
import { normalizeConfigResponse } from '../src/services/api/transformers';

const originalGet = apiClient.get;
const originalPut = apiClient.put;

const callerOwnedConfig = {
  apiKey: 'claude-secret',
  baseUrl: 'https://api.anthropic.com',
};

afterEach(() => {
  apiClient.get = originalGet;
  apiClient.put = originalPut;
});

describe('Claude fingerprint profile', () => {
  test('normalizes the backend field and exposes the CLI profile resource flag', () => {
    const config = normalizeConfigResponse({
      'claude-api-key': [
        {
          'api-key': 'claude-secret',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
        },
      ],
    });

    expect(config.claudeApiKeys).toEqual([
      {
        apiKey: 'claude-secret',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
      },
    ]);
    expect(claudeToResource(config.claudeApiKeys![0], 0).flags.claudeCodeCliProfile).toBe(true);
  });

  test('serializes the opt-in profile when creating a Claude key', async () => {
    const calls: Array<{ url: string; data?: unknown }> = [];
    apiClient.get = (async () => ({ 'claude-api-key': [] })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      calls.push({ url, data });
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.createClaudeConfig({
      ...callerOwnedConfig,
      fingerprintProfile: 'claude-code-cli',
    });

    expect(calls).toEqual([
      {
        url: '/claude-api-key',
        data: [
          {
            'api-key': 'claude-secret',
            'base-url': 'https://api.anthropic.com',
            'fingerprint-profile': 'claude-code-cli',
          },
        ],
      },
    ]);
  });

  test('clears the profile and deprecated CCH field when saving caller-owned mode', async () => {
    const calls: Array<{ url: string; data?: unknown }> = [];
    apiClient.get = (async () => ({
      'claude-api-key': [
        {
          'api-key': 'claude-secret',
          'base-url': 'https://api.anthropic.com',
          'fingerprint-profile': 'claude-code-cli',
          'experimental-cch-signing': true,
          'future-field': 'preserved',
        },
      ],
    })) as typeof apiClient.get;
    apiClient.put = (async (url: string, data?: unknown) => {
      calls.push({ url, data });
      return undefined;
    }) as typeof apiClient.put;

    await providersApi.updateClaudeConfig(
      'claude-secret',
      'https://api.anthropic.com',
      callerOwnedConfig
    );

    expect(calls).toEqual([
      {
        url: '/claude-api-key',
        data: [
          {
            'api-key': 'claude-secret',
            'base-url': 'https://api.anthropic.com',
            'future-field': 'preserved',
          },
        ],
      },
    ]);
  });
});
