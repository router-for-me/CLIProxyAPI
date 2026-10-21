/**
 * 自定义请求头处理工具
 */

export interface HeaderEntry {
  key: string;
  value: string;
}

export function buildHeaderObject(
  input?: HeaderEntry[] | Record<string, string | undefined | null>
): Record<string, string> {
  if (!input) return {};

  if (Array.isArray(input)) {
    return input.reduce<Record<string, string>>((acc, item) => {
      const key = item?.key?.trim();
      const value = item?.value?.trim();
      if (key && value !== undefined && value !== null && value !== '') {
        acc[key] = value;
      }
      return acc;
    }, {});
  }

  return Object.entries(input).reduce<Record<string, string>>((acc, [rawKey, rawValue]) => {
    const key = rawKey?.trim();
    const value = typeof rawValue === 'string' ? rawValue.trim() : rawValue;
    if (key && value !== undefined && value !== null && value !== '') {
      acc[key] = String(value);
    }
    return acc;
  }, {});
}

export function hasHeader(
  headers: Record<string, unknown> | null | undefined,
  name: string
): boolean {
  if (!headers) return false;
  const target = name.toLowerCase();
  return Object.keys(headers).some((key) => key.toLowerCase() === target);
}

