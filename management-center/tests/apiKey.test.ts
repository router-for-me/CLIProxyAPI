import { describe, expect, test } from 'bun:test';
import { generateSecureApiKey } from '../src/utils/apiKey';

describe('API key generation', () => {
  test('generates a 51-character key with the expected prefix and charset', () => {
    const apiKey = generateSecureApiKey();

    expect(apiKey).toHaveLength(51);
    expect(apiKey).toMatch(/^sk-[A-Za-z0-9]{48}$/);
  });

  test('generates distinct keys', () => {
    const apiKeys = Array.from({ length: 100 }, () => generateSecureApiKey());

    expect(new Set(apiKeys).size).toBe(apiKeys.length);
  });
});
