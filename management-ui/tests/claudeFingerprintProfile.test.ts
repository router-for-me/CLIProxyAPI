import { describe, expect, test } from 'bun:test';
import { claudeToResource } from '../src/features/providers/adapters';
import { normalizeConfigResponse } from '../src/services/api/transformers';

describe('Claude fingerprint profile', () => {
  test('normalizes the backend field and exposes the CLI profile resource flag', () => {
    const config = normalizeConfigResponse({
      'api-keys': {
        claude: [
          {
            name: 'claude-1',
            'base-url': 'https://api.anthropic.com',
            keys: [{ 'api-key': 'claude-secret', 'fingerprint-profile': 'claude-code-cli' }],
          },
        ],
      },
    });

    expect(config.claudeApiKeys).toMatchObject([
      {
        apiKey: 'claude-secret',
        baseUrl: 'https://api.anthropic.com',
        fingerprintProfile: 'claude-code-cli',
      },
    ]);
    expect(claudeToResource(config.claudeApiKeys![0], 0).flags.claudeCodeCliProfile).toBe(true);
  });
});
