type ResetGrantRandomSource = Partial<Pick<Crypto, 'randomUUID' | 'getRandomValues'>>;

const REQUEST_ID_RANDOM_BYTES = 16;

/** 普通 HTTP 页面没有 randomUUID，使用同样安全的随机字节生成请求 ID。 */
export function generateResetGrantRequestId(
  source: ResetGrantRandomSource | undefined = globalThis.crypto
): string {
  if (typeof source?.randomUUID === 'function') return source.randomUUID();
  if (typeof source?.getRandomValues !== 'function') {
    throw new Error('Secure random source is unavailable');
  }

  const bytes = source.getRandomValues(new Uint8Array(REQUEST_ID_RANDOM_BYTES));
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
}
