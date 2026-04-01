/**
 * 验证工具函数
 */

/**
 * 验证 URL 格式
 */
export function isValidUrl(url: string): boolean {
  try {
    new URL(url);
    return true;
  } catch {
    return false;
  }
}

/**
 * 验证 API Base URL
 */
export function isValidApiBase(apiBase: string): boolean {
  if (!apiBase) return false;

  // 允许 http/https 协议
  const urlPattern = /^https?:\/\/.+/i;
  return urlPattern.test(apiBase);
}

/**
 * 验证 API Key 格式
 */
export function isValidApiKey(key: string): boolean {
  if (!key || key.length < 8) return false;

  // 基础验证：不包含空格
  return !/\s/.test(key);
}

/**
 * 验证 API Key 字符集（仅允许 ASCII 可见字符）
 */
export function isValidApiKeyCharset(key: string): boolean {
  if (!key) return false;
  return /^[\x21-\x7E]+$/.test(key);
}

/**
 * 验证 JSON 格式
 */
export function isValidJson(str: string): boolean {
  try {
    JSON.parse(str);
    return true;
  } catch {
    return false;
  }
}

/**
 * 验证 Email 格式
 */
export function isValidEmail(email: string): boolean {
  const emailPattern = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
  return emailPattern.test(email);
}
