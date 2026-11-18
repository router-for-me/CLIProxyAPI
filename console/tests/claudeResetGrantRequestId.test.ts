import { describe, expect, test } from 'bun:test';
import { generateResetGrantRequestId } from '../src/features/quota/providers/claude/resetGrantRequestId';
import { ANTHROPIC_RESET_REQUEST_ID_RE } from '../src/services/api/claudeResetGrants';

type RandomSource = Partial<Pick<Crypto, 'randomUUID' | 'getRandomValues'>>;

function withGlobalCrypto(source: unknown, run: () => void) {
  const descriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  try {
    Object.defineProperty(globalThis, 'crypto', { configurable: true, value: source });
    run();
  } finally {
    if (descriptor) Object.defineProperty(globalThis, 'crypto', descriptor);
    else Reflect.deleteProperty(globalThis, 'crypto');
  }
}

describe('Claude reset grant secure request ID', () => {
  test('native randomUUID takes priority and preserves its crypto receiver', () => {
    const uuid = '01234567-89ab-4cde-8fab-0123456789ab';
    let nativeCalls = 0;
    const source: RandomSource = {
      randomUUID() {
        expect(this).toBe(source);
        nativeCalls++;
        return uuid;
      },
      getRandomValues() {
        throw new Error('不应调用回退随机源');
      },
    };
    expect(generateResetGrantRequestId(source)).toBe(uuid);
    expect(generateResetGrantRequestId(source)).toMatch(ANTHROPIC_RESET_REQUEST_ID_RE);
    expect(nativeCalls).toBe(2);
  });

  test('fallback fills exactly 16 bytes and preserves zero-padded lowercase hexadecimal', () => {
    let randomCalls = 0;
    const source: RandomSource = {
      getRandomValues(bytes) {
        expect(this).toBe(source);
        expect(bytes).toBeInstanceOf(Uint8Array);
        if (!(bytes instanceof Uint8Array)) throw new Error('预期 Uint8Array');
        expect(bytes.byteLength).toBe(16);
        bytes.set([0, 1, 2, 15, 16, 31, 127, 128, 255, 254, 32, 64, 3, 4, 5, 6]);
        randomCalls++;
        return bytes;
      },
    };
    const requestId = generateResetGrantRequestId(source);
    expect(requestId).toBe('0001020f101f7f80fffe204003040506');
    expect(requestId).toMatch(/^[0-9a-f]{32}$/);
    expect(requestId).toMatch(ANTHROPIC_RESET_REQUEST_ID_RE);
    expect(randomCalls).toBe(1);
  });

  test('a source without either secure capability fails with fixed safe error text', () => {
    for (const source of [{}, { randomUUID: undefined, getRandomValues: undefined }]) {
      expect(() => generateResetGrantRequestId(source)).toThrow(
        new Error('Secure random source is unavailable')
      );
    }
  });

  test('default source is read from global crypto at call time, not module import time', () => {
    const firstUuid = '01234567-89ab-4cde-8fab-0123456789ab';
    const secondUuid = 'fedcba98-7654-4321-8abc-fedcba987654';
    withGlobalCrypto({ randomUUID: () => firstUuid }, () => {
      expect(generateResetGrantRequestId()).toBe(firstUuid);
    });
    withGlobalCrypto({ randomUUID: () => secondUuid }, () => {
      expect(generateResetGrantRequestId()).toBe(secondUuid);
      expect(generateResetGrantRequestId(undefined)).toBe(secondUuid);
    });
  });

  test('missing global crypto refuses generation without insecure randomness', () => {
    withGlobalCrypto(undefined, () => {
      expect(() => generateResetGrantRequestId()).toThrow(
        new Error('Secure random source is unavailable')
      );
    });
  });

  test('native randomUUID failure is not hidden by switching to a different source', () => {
    const failure = new Error('原生安全随机源失败');
    let fallbackCalls = 0;
    expect(() =>
      generateResetGrantRequestId({
        randomUUID() {
          throw failure;
        },
        getRandomValues() {
          fallbackCalls++;
          throw new Error('不应调用回退随机源');
        },
      })
    ).toThrow(failure);
    expect(fallbackCalls).toBe(0);
  });

  test('getRandomValues failure does not produce a request ID', () => {
    const failure = new Error('安全随机填充失败');
    expect(() =>
      generateResetGrantRequestId({
        getRandomValues() {
          throw failure;
        },
      })
    ).toThrow(failure);
  });
});
