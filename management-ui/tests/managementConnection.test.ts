import { describe, expect, test } from 'bun:test';
import { computeApiUrl, normalizeApiBase } from '../src/utils/connection';
import { resolvePluginAssetURL } from '../src/features/plugins/pluginResources';

describe('v8-only management connections', () => {
  test.each([
    ['localhost:8317', 'http://localhost:8317'],
    [' https://proxy.example/ ', 'https://proxy.example'],
    ['https://proxy.example/v8/management/', 'https://proxy.example'],
    ['https://proxy.example/gateway/v8/management///', 'https://proxy.example/gateway'],
  ])('normalizes %s without changing the deployment prefix', (input, expected) => {
    expect(normalizeApiBase(input)).toBe(expected);
    expect(computeApiUrl(input)).toBe(`${expected}/v8/management`);
  });

  test('does not create a URL for empty input', () => {
    expect(computeApiUrl('  ')).toBe('');
  });

  test('does not silently adapt a legacy management URL', () => {
    expect(normalizeApiBase('https://proxy.example/v0/management')).toBe(
      'https://proxy.example/v0/management'
    );
  });

  test('preserves backend-declared plugin resource and custom extension paths', () => {
    for (const path of ['/v0/resource/plugins/example/index.html', '/v0/management/example']) {
      expect(resolvePluginAssetURL(path, 'https://proxy.example/v8/management')).toBe(
        `https://proxy.example${path}`
      );
    }
  });
});
