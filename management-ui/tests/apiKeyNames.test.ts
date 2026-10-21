import { afterEach, beforeEach, describe, expect, test } from 'bun:test';
import { createHash } from 'node:crypto';
import {
  apiKeyNameFingerprint,
  readApiKeyNames,
  saveApiKeyName,
} from '../src/features/config/apiKeyNames';
import { obfuscatedStorage } from '../src/services/storage/secureStorage';

const originalStorage = Object.getOwnPropertyDescriptor(globalThis, 'localStorage');
let values: Map<string, string>;

beforeEach(() => {
  values = new Map();
  Object.defineProperty(globalThis, 'localStorage', {
    configurable: true,
    value: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    },
  });
});

afterEach(() => {
  if (originalStorage) Object.defineProperty(globalThis, 'localStorage', originalStorage);
  else Reflect.deleteProperty(globalThis, 'localStorage');
});

describe('local API key names', () => {
  test('persists optional names by server and key, not list position', () => {
    expect(saveApiKeyName('https://one.test', 'fixture-key-a', ' Alice ')).toBe(true);
    saveApiKeyName('https://one.test', 'fixture-key-b', 'Bob');
    saveApiKeyName('https://two.test', 'fixture-key-a', 'Other');
    expect(readApiKeyNames('https://one.test')).toEqual({
      [apiKeyNameFingerprint('https://one.test', 'fixture-key-a')]: 'Alice',
      [apiKeyNameFingerprint('https://one.test', 'fixture-key-b')]: 'Bob',
    });
    expect(
      readApiKeyNames('https://two.test')[
        apiKeyNameFingerprint('https://two.test', 'fixture-key-a')
      ]
    ).toBe('Other');
    expect([...values.values()].join('')).not.toContain('fixture-key-a');
  });

  test('clears names without removing other entries', () => {
    saveApiKeyName('server', 'one', 'First');
    saveApiKeyName('server', 'two', 'Second');
    saveApiKeyName('server', 'one', '  ');
    expect(readApiKeyNames('server')).toEqual({
      [apiKeyNameFingerprint('server', 'two')]: 'Second',
    });
    saveApiKeyName('server', 'two', '');
    expect(values.size).toBe(0);
  });

  test('handles special property names', () => {
    saveApiKeyName('server', '__proto__', 'Special');
    expect(readApiKeyNames('server')[apiKeyNameFingerprint('server', '__proto__')]).toBe('Special');
    expect(Object.hasOwn(readApiKeyNames('server'), '__proto__')).toBe(false);
  });

  test('ignores malformed data and invalid entries', () => {
    values.set('api-key-names:v1:server', '{invalid');
    expect(readApiKeyNames('server')).toEqual({});
    const fingerprint = apiKeyNameFingerprint('server', 'valid');
    obfuscatedStorage.setItem('api-key-names:v1:server', {
      [fingerprint]: 'Name',
      ['a'.repeat(64)]: 4,
      ['b'.repeat(64)]: '',
      'not-a-fingerprint': 'Invalid',
    });
    expect(readApiKeyNames('server')).toEqual({ [fingerprint]: 'Name' });
    obfuscatedStorage.setItem('api-key-names:v1:server', ['Name']);
    expect(readApiKeyNames('server')).toEqual({});
  });

  test('stores only SHA-256 fingerprints even after reversing storage obfuscation', () => {
    const apiBase = 'https://one.test';
    const apiKey = 'sk-fixture-not-a-real-credential';
    saveApiKeyName(apiBase, apiKey, 'Fixture');
    const stored = obfuscatedStorage.getItem<Record<string, string>>(`api-key-names:v1:${apiBase}`);
    const expected = createHash('sha256')
      .update(JSON.stringify(['api-key-name', apiBase, apiKey]))
      .digest('hex');
    expect(stored).toEqual({ [expected]: 'Fixture' });
    expect(JSON.stringify(stored)).not.toContain(apiKey);
    expect(Object.keys(stored!)).toEqual([apiKeyNameFingerprint(apiBase, apiKey)]);
    expect(apiKeyNameFingerprint('https://two.test', apiKey)).not.toBe(expected);
  });

  test('matches by the entire key rather than its masked display', () => {
    const keys = ['sk-same-first-middle-a-last', 'sk-same-first-middle-b-last'];
    saveApiKeyName('server', keys[0], 'First');
    saveApiKeyName('server', keys[1], 'Second');
    const names = readApiKeyNames('server');
    expect(names[apiKeyNameFingerprint('server', keys[0])]).toBe('First');
    expect(names[apiKeyNameFingerprint('server', keys[1])]).toBe('Second');
  });

  test('does not need Web Crypto or a secure context', () => {
    const originalCrypto = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
    try {
      Object.defineProperty(globalThis, 'crypto', { configurable: true, value: undefined });
      expect(saveApiKeyName('http://lan.test', 'fixture-key', 'LAN')).toBe(true);
      expect(
        readApiKeyNames('http://lan.test')[apiKeyNameFingerprint('http://lan.test', 'fixture-key')]
      ).toBe('LAN');
    } finally {
      if (originalCrypto) Object.defineProperty(globalThis, 'crypto', originalCrypto);
      else Reflect.deleteProperty(globalThis, 'crypto');
    }
  });

  test('reports unavailable storage instead of throwing', () => {
    Object.defineProperty(globalThis, 'localStorage', {
      configurable: true,
      get() {
        throw new Error('Storage blocked');
      },
    });
    expect(readApiKeyNames('server')).toEqual({});
    expect(saveApiKeyName('server', 'key', 'Name')).toBe(false);
  });
});
