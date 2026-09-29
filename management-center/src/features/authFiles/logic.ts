/**
 * 认证文件列表纯逻辑：通配搜索、字段匹配、排序。
 * React-free —— 由 tests/authFilesListLogic.test.ts 直接消费。
 */

import type { AuthFileItem } from '@/types';
import { resolveAuthProvider } from '@/utils/quota';
import {
  QUOTA_PROVIDER_TYPES,
  normalizeProviderKey,
  type AuthFileQuotaFilter,
  type QuotaProviderType,
} from './constants';
import { deriveAuthFileIdentity } from './identity';
import type { AuthFilesSortMode } from './uiState';

const escapeWildcardSearchSegment = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/** 不含 '*' 时返回 null（走 includes 路径）。刻意不加 ^/$ 锚点——保持子串语义。 */
export const buildWildcardSearch = (value: string): RegExp | null => {
  if (!value.includes('*')) return null;
  const pattern = value.split('*').map(escapeWildcardSearchSegment).join('.*');
  return new RegExp(pattern, 'i');
};

/** “全部”展示每张受支持卡片自己的额度；指定提供商时只展示匹配的额度。 */
export const resolveAuthFileQuotaType = (
  file: AuthFileItem,
  filter: AuthFileQuotaFilter
): QuotaProviderType | null => {
  if (!filter) return null;

  const provider = resolveAuthProvider(file);
  if (!QUOTA_PROVIDER_TYPES.has(provider as QuotaProviderType)) return null;
  if (filter !== 'all' && provider !== filter) return null;

  return provider as QuotaProviderType;
};

/**
 * 搜索 haystack：文件名 + 类型 + 提供方 + 账号邮箱 + 项目 ID。
 * 显式不含 account —— api-key 凭证的 account 就是 API key 本身，见 identity.ts。
 */
export const matchesAuthFileSearch = (
  file: AuthFileItem,
  term: string,
  wildcard: RegExp | null
): boolean => {
  if (!term) return true;
  const needle = term.toLowerCase();
  return [file.name, file.type, file.provider, file.email, file.projectId].some((value) => {
    const content = (value || '').toString();
    return wildcard ? wildcard.test(content) : content.toLowerCase().includes(needle);
  });
};

/** 返回新数组，不改动入参。未知 mode 原序返回拷贝。 */
export const sortAuthFiles = (files: AuthFileItem[], mode: AuthFilesSortMode): AuthFileItem[] => {
  const copy = [...files];
  if (mode === 'default') {
    copy.sort((a, b) => {
      const providerA = normalizeProviderKey(String(a.provider ?? a.type ?? 'unknown'));
      const providerB = normalizeProviderKey(String(b.provider ?? b.type ?? 'unknown'));
      const providerCompare = providerA.localeCompare(providerB);
      if (providerCompare !== 0) return providerCompare;
      return a.name.localeCompare(b.name);
    });
  } else if (mode === 'az') {
    // 按卡片主行排（有账号时即 email），所见即所排；同值用文件名决胜。
    // 装饰一次，避免在比较器里重复派生。
    const keys = new Map(copy.map((file) => [file, deriveAuthFileIdentity(file).primary]));
    copy.sort(
      (a, b) => (keys.get(a) ?? '').localeCompare(keys.get(b) ?? '') || a.name.localeCompare(b.name)
    );
  } else if (mode === 'priority') {
    copy.sort((a, b) => {
      const pa = typeof a.priority === 'number' ? a.priority : 0;
      const pb = typeof b.priority === 'number' ? b.priority : 0;
      return pb - pa; // 高优先级排前面
    });
  }
  return copy;
};
