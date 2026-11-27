import { afterEach, describe, expect, test } from 'bun:test';
import { deobfuscateData, isCurrentObfuscated, obfuscateData } from '@/utils/encryption';

const originalWindow = Object.getOwnPropertyDescriptor(globalThis, 'window');
const originalNavigator = Object.getOwnPropertyDescriptor(globalThis, 'navigator');

afterEach(() => {
  for (const [key, descriptor] of [
    ['window', originalWindow],
    ['navigator', originalNavigator],
  ] as const) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor);
    else Reflect.deleteProperty(globalThis, key);
  }
});

describe('v2 storage obfuscation', () => {
  test('new values remain readable when the browser UA changes', () => {
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: { location: { host: 'panel.example.com' } },
    });
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      value: { userAgent: 'Firefox/158.0' },
    });
    // Long enough to exercise the UA-dependent portion of the old XOR key.
    const value = JSON.stringify({ managementKey: 'test-only-'.repeat(100), label: '测试' });
    const encoded = obfuscateData(value);
    expect(encoded.startsWith('enc::v2::')).toBe(true);
    Object.defineProperty(globalThis, 'navigator', {
      configurable: true,
      value: { userAgent: 'Firefox/159.0' },
    });
    expect(deobfuscateData(encoded)).toBe(value);
    expect(obfuscateData(value)).toBe(encoded);
  });

  test('auth format detection rejects legacy and plaintext values', () => {
    expect(isCurrentObfuscated('enc::v1::old')).toBe(false);
    expect(isCurrentObfuscated('plaintext')).toBe(false);
    expect(isCurrentObfuscated(obfuscateData('test-key'))).toBe(true);
  });
});
