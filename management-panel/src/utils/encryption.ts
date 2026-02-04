/**
 * 加密工具函数
 * 从原项目 src/utils/secure-storage.js 迁移
 */

const ENC_PREFIX = 'enc::v1::';
const SECRET_SALT = 'cli-proxy-api-webui::secure-storage';

let cachedKeyBytes: Uint8Array | null = null;

function encodeText(text: string): Uint8Array {
  const encoder = new TextEncoder();
  return encoder.encode(text);
}

function decodeText(bytes: Uint8Array): string {
  const decoder = new TextDecoder();
  return decoder.decode(bytes);
}

function getKeyBytes(): Uint8Array {
  if (cachedKeyBytes) return cachedKeyBytes;

  try {
    const host = window.location.host;
    const ua = navigator.userAgent;
    cachedKeyBytes = encodeText(`${SECRET_SALT}|${host}|${ua}`);
  } catch (error) {
    console.warn('Encryption fallback to simple key:', error);
    cachedKeyBytes = encodeText(SECRET_SALT);
  }

  return cachedKeyBytes;
}

function xorBytes(data: Uint8Array, keyBytes: Uint8Array): Uint8Array {
  const result = new Uint8Array(data.length);
  for (let i = 0; i < data.length; i++) {
    result[i] = data[i] ^ keyBytes[i % keyBytes.length];
  }
  return result;
}

function toBase64(bytes: Uint8Array): string {
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

function fromBase64(base64: string): Uint8Array {
  const binary = atob(base64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/**
 * 加密数据
 */
export function encryptData(value: string): string {
  if (!value) return value;

  try {
    const keyBytes = getKeyBytes();
    const encrypted = xorBytes(encodeText(value), keyBytes);
    return `${ENC_PREFIX}${toBase64(encrypted)}`;
  } catch (error) {
    console.warn('Encryption failed, fallback to plaintext:', error);
    return value;
  }
}

/**
 * 解密数据
 */
export function decryptData(payload: string): string {
  if (!payload || !payload.startsWith(ENC_PREFIX)) {
    return payload;
  }

  try {
    const encodedBody = payload.slice(ENC_PREFIX.length);
    const encrypted = fromBase64(encodedBody);
    const decrypted = xorBytes(encrypted, getKeyBytes());
    return decodeText(decrypted);
  } catch (error) {
    console.warn('Decryption failed, return as-is:', error);
    return payload;
  }
}

/**
 * 检查是否已加密
 */
export function isEncrypted(value: string): boolean {
  return value?.startsWith(ENC_PREFIX) || false;
}
