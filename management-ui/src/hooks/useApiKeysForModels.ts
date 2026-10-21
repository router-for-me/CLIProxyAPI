import { useCallback, useEffect, useRef } from 'react';
import { apiKeysApi } from '@/services/api/apiKeys';
import { useAuthStore, useConfigStore } from '@/stores';

const normalizeApiKeyList = (input: unknown): string[] => {
  if (!Array.isArray(input)) return [];
  const seen = new Set<string>();
  const keys: string[] = [];

  input.forEach((item) => {
    const record =
      item !== null && typeof item === 'object' && !Array.isArray(item)
        ? (item as Record<string, unknown>)
        : null;
    const value =
      typeof item === 'string'
        ? item
        : record
          ? (record['api-key'] ?? record['apiKey'] ?? record.key ?? record.Key)
          : '';
    const trimmed = String(value ?? '').trim();
    if (!trimmed || seen.has(trimmed)) return;
    seen.add(trimmed);
    keys.push(trimmed);
  });

  return keys;
};

/**
 * 解析用于 /models 探测的 API 密钥列表:
 * 优先取已缓存配置中的 api-keys,否则回退到 /api-keys 接口,结果按连接缓存。
 */
export function useApiKeysForModels() {
  const apiBase = useAuthStore((state) => state.apiBase);
  const configApiKeys = useConfigStore((state) => state.config?.apiKeys);
  const cacheRef = useRef<string[]>([]);

  useEffect(() => {
    cacheRef.current = [];
  }, [apiBase, configApiKeys]);

  return useCallback(
    async ({ force = false }: { force?: boolean } = {}) => {
      if (force) {
        cacheRef.current = [];
      }
      if (cacheRef.current.length) {
        return cacheRef.current;
      }

      const configKeys = normalizeApiKeyList(configApiKeys);
      if (configKeys.length) {
        cacheRef.current = configKeys;
        return configKeys;
      }

      try {
        const list = await apiKeysApi.list();
        const normalized = normalizeApiKeyList(list);
        if (normalized.length) {
          cacheRef.current = normalized;
        }
        return normalized;
      } catch {
        return [];
      }
    },
    [configApiKeys]
  );
}
