import type { TFunction } from 'i18next';
import iconAntigravity from '@/assets/icons/antigravity.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconCodex from '@/assets/icons/codex.svg';
import iconMeta from '@/assets/icons/meta.svg';
import iconDevin from '@/assets/icons/devin.svg';
import iconDevinDark from '@/assets/icons/devin-dark.svg';
import iconGemini from '@/assets/icons/gemini.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconGrokDark from '@/assets/icons/grok-dark.svg';
import iconIflow from '@/assets/icons/iflow.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconVertex from '@/assets/icons/vertex.svg';
import type { AuthFileItem, ResolvedTheme, ThemeColors } from '@/types';
import { normalizeOAuthProviderKey } from '@/utils/providerKeys';
import { parseTimestamp } from '@/utils/timestamp';
import { TYPE_COLORS } from '@/utils/quota';

export type { ResolvedTheme, ThemeColors, TypeColorSet } from '@/types';
export type AuthFileModelItem = {
  id: string;
  display_name?: string;
  type?: string;
  owned_by?: string;
};
export type AuthFileIconAsset = string | { light: string; dark: string };

export type QuotaProviderType =
  | 'antigravity'
  | 'claude'
  | 'codex'
  | 'devin'
  | 'kimi'
  | 'meta'
  | 'plugin'
  | 'xai';
export type AuthFileQuotaFilter = string | null;
export type OAuthConfigLoadError = 'loading' | 'unsupported' | 'load' | null;

export const QUOTA_PROVIDER_TYPES = new Set<QuotaProviderType>([
  'meta',
  'antigravity',
  'claude',
  'codex',
  'devin',
  'kimi',
  'plugin',
  'xai',
]);

export const OAUTH_PROVIDER_PRESETS = [
  'meta',
  'vertex',
  'aistudio',
  'antigravity',
  'xai',
  'claude',
  'codex',
  'devin',
  'kimi',
];

const OAUTH_PROVIDER_EXCLUDES = new Set(['all', 'unknown', 'empty']);

export const MIN_CARD_PAGE_SIZE = 3;
export const MAX_CARD_PAGE_SIZE = 30;

export const INTEGER_STRING_PATTERN = /^[+-]?\d+$/;
export const TRUTHY_TEXT_VALUES = new Set(['true', '1', 'yes', 'y', 'on']);
export const FALSY_TEXT_VALUES = new Set(['false', '0', 'no', 'n', 'off']);
export const AUTH_FILE_WEBSOCKET_PROVIDERS = new Set(['codex', 'xai']);
export const AUTH_FILE_USING_API_PROVIDERS = new Set(['xai']);
export const AUTH_FILE_MANUAL_REFRESH_PROVIDERS = new Set([
  'meta',
  'antigravity',
  'claude',
  'codex',
  'kimi',
  'xai',
]);

// 标签类型颜色配置：权威版本在 @/utils/quota/constants.ts，此处仅转发
export { TYPE_COLORS } from '@/utils/quota';

export const AUTH_FILE_ICONS: Record<string, AuthFileIconAsset> = {
  antigravity: iconAntigravity,
  aistudio: iconGemini,
  claude: iconClaude,
  codex: iconCodex,
  meta: iconMeta,
  devin: { light: iconDevin, dark: iconDevinDark },
  gemini: iconGemini,
  xai: { light: iconGrok, dark: iconGrokDark },
  iflow: iconIflow,
  kimi: { light: iconKimiDark, dark: iconKimiLight },
  qwen: iconQwen,
  vertex: iconVertex,
};

export const clampCardPageSize = (value: number) =>
  Math.min(MAX_CARD_PAGE_SIZE, Math.max(MIN_CARD_PAGE_SIZE, Math.round(value)));

export const normalizeProviderKey = normalizeOAuthProviderKey;

export const supportsAuthFileManualRefresh = (provider: unknown): boolean =>
  AUTH_FILE_MANUAL_REFRESH_PROVIDERS.has(normalizeProviderKey(String(provider ?? '')));

export const buildOAuthProviderOptions = (values: Iterable<unknown>): string[] => {
  const extraProviders = new Set<string>();

  Array.from(values).forEach((value) => {
    const key = normalizeProviderKey(String(value ?? ''));
    if (!key || OAUTH_PROVIDER_EXCLUDES.has(key)) return;
    extraProviders.add(key);
  });

  const baseSet = new Set(OAUTH_PROVIDER_PRESETS.map((value) => normalizeProviderKey(value)));
  const extraList = Array.from(extraProviders)
    .filter((value) => !baseSet.has(value))
    .sort((a, b) => a.localeCompare(b));

  return [...OAUTH_PROVIDER_PRESETS, ...extraList];
};

export const getAuthFileStatusMessage = (file: AuthFileItem): string => {
  const raw = file['status_message'] ?? file.statusMessage;
  if (typeof raw === 'string') return raw.trim();
  if (raw == null) return '';
  return String(raw).trim();
};

/** 这些 status_message 视为健康，不触发告警态。 */
export const HEALTHY_AUTH_FILE_STATUS_MESSAGES = new Set([
  'ok',
  'healthy',
  'ready',
  'success',
  'available',
]);

/** 是否存在非健康的 status_message（卡片告警态 / 谱条琥珀色共用判定）。 */
export const hasAuthFileStatusWarning = (file: AuthFileItem): boolean => {
  const message = getAuthFileStatusMessage(file);
  return Boolean(message) && !HEALTHY_AUTH_FILE_STATUS_MESSAGES.has(message.toLowerCase());
};

/**
 * 是否为需要用户处理的问题凭证。
 * 主动停用是独立状态，不应进入“问题”筛选或“删除问题凭证”的批量操作。
 */
export const isProblemAuthFile = (file: AuthFileItem): boolean => {
  const status = typeof file.status === 'string' ? file.status.trim().toLowerCase() : '';
  if (file.disabled === true || status === 'disabled') return false;
  return file.unavailable === true || status === 'error' || hasAuthFileStatusWarning(file);
};

export const getTypeLabel = (t: TFunction, type: string): string => {
  const providerKey = normalizeProviderKey(type);
  const key = `auth_files.filter_${providerKey}`;
  const translated = t(key);
  if (translated !== key) return translated;
  if (providerKey === 'iflow') return 'iFlow';
  return type.charAt(0).toUpperCase() + type.slice(1);
};

export const getTypeColor = (type: string, resolvedTheme: ResolvedTheme): ThemeColors => {
  const set = TYPE_COLORS[normalizeProviderKey(type)] || TYPE_COLORS.unknown;
  return resolvedTheme === 'dark' && set.dark ? set.dark : set.light;
};

export const getAuthFileIcon = (type: string, resolvedTheme: ResolvedTheme): string | null => {
  const iconEntry = AUTH_FILE_ICONS[normalizeProviderKey(type)];
  if (!iconEntry) return null;
  return typeof iconEntry === 'string'
    ? iconEntry
    : resolvedTheme === 'dark'
      ? iconEntry.dark
      : iconEntry.light;
};

// 与 AI 提供商界面（PROVIDER_LOGOS 的 themeSurface）保持一致：
// 这些提供商的图标底座颜色随主题切换（浅色主题黑底，深色主题白底）
export const THEME_SURFACE_ICON_PROVIDERS = new Set(['kimi']);

export const isThemeSurfaceIconProvider = (type: string): boolean =>
  THEME_SURFACE_ICON_PROVIDERS.has(normalizeProviderKey(type));

export const getThemeSurfaceIconBackground = (resolvedTheme: ResolvedTheme): string =>
  resolvedTheme === 'dark' ? '#ffffff' : '#000000';

export const parsePriorityValue = (value: unknown): number | undefined => {
  if (typeof value === 'number') {
    return Number.isInteger(value) ? value : undefined;
  }

  if (typeof value !== 'string') return undefined;
  const trimmed = value.trim();
  if (!trimmed || !INTEGER_STRING_PATTERN.test(trimmed)) return undefined;
  const parsed = Number.parseInt(trimmed, 10);
  return Number.isSafeInteger(parsed) ? parsed : undefined;
};

export const parseDisableCoolingValue = (value: unknown): boolean | undefined => {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'number' && Number.isFinite(value)) return value !== 0;
  if (typeof value !== 'string') return undefined;

  const normalized = value.trim().toLowerCase();
  if (!normalized) return undefined;
  if (TRUTHY_TEXT_VALUES.has(normalized)) return true;
  if (FALSY_TEXT_VALUES.has(normalized)) return false;
  return undefined;
};

export const readAuthFileDisableCooling = (value: Record<string, unknown>): boolean => {
  const canonical = parseDisableCoolingValue(value.disable_cooling);
  if (canonical !== undefined) return canonical;
  return parseDisableCoolingValue(value['disable-cooling']) ?? false;
};

export const supportsAuthFileWebsockets = (providerKey: string): boolean =>
  AUTH_FILE_WEBSOCKET_PROVIDERS.has(normalizeProviderKey(providerKey));

export const readAuthFileWebsockets = (value: Record<string, unknown>): boolean =>
  parseDisableCoolingValue(value.websockets ?? value.websocket) ?? false;

export const applyAuthFileWebsockets = (
  value: Record<string, unknown>,
  websockets: boolean
): Record<string, unknown> => {
  const next = { ...value };
  delete next.websocket;
  next.websockets = websockets;
  return next;
};

export const supportsAuthFileUsingApi = (providerKey: string): boolean =>
  AUTH_FILE_USING_API_PROVIDERS.has(normalizeProviderKey(providerKey));

export const readAuthFileUsingApi = (value: Record<string, unknown>): boolean =>
  parseDisableCoolingValue(value.using_api) ?? false;

export const applyAuthFileUsingApi = (
  value: Record<string, unknown>,
  usingApi: boolean
): Record<string, unknown> => ({ ...value, using_api: usingApi });

export function isRuntimeOnlyAuthFile(file: AuthFileItem): boolean {
  const raw = file['runtime_only'] ?? file.runtimeOnly;
  if (typeof raw === 'boolean') return raw;
  if (typeof raw === 'string') return raw.trim().toLowerCase() === 'true';
  return false;
}

export const formatModified = (item: AuthFileItem): string => {
  const raw = item['modtime'] ?? item.modified;
  if (!raw) return '-';
  const asNumber = Number(raw);
  const date =
    Number.isFinite(asNumber) && !Number.isNaN(asNumber)
      ? new Date(asNumber < 1e12 ? asNumber * 1000 : asNumber)
      : (parseTimestamp(raw) ?? new Date(String(raw)));
  return Number.isNaN(date.getTime()) ? '-' : date.toLocaleString();
};

// 检查模型是否被 OAuth 排除
export const isModelExcluded = (
  modelId: string,
  providerType: string,
  excluded: Record<string, string[]>
): boolean => {
  const providerKey = normalizeProviderKey(providerType);
  const excludedModels = excluded[providerKey] || excluded[providerType] || [];
  return excludedModels.some((pattern) => {
    if (pattern.includes('*')) {
      // 支持通配符匹配：先转义正则特殊字符，再将 * 视为通配符
      const regexSafePattern = pattern
        .split('*')
        .map((segment) => segment.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
        .join('.*');
      const regex = new RegExp(`^${regexSafePattern}$`, 'i');
      return regex.test(modelId);
    }
    return pattern.toLowerCase() === modelId.toLowerCase();
  });
};
