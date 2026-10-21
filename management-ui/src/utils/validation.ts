/**
 * 验证工具函数
 */

/**
 * 验证 API Key 字符集（仅允许 ASCII 可见字符）
 */
export function isValidApiKeyCharset(key: string): boolean {
  if (!key) return false;
  return /^[\x21-\x7E]+$/.test(key);
}
