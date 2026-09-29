/**
 * 常量定义
 * 从原项目 src/utils/constants.js 迁移
 */

import type { Language } from '@/types';

const defineLanguageOrder = <T extends readonly Language[]>(
  languages: T & ([Language] extends [T[number]] ? unknown : never)
) => languages;

// 缓存过期时间（毫秒）
export const CACHE_EXPIRY_MS = 30 * 1000; // 与基线保持一致，减少管理端压力

// 网络与版本信息
export const DEFAULT_API_PORT = 8317;
export const MANAGEMENT_API_PREFIX = '/v0/management';
export const REQUEST_TIMEOUT_MS = 30 * 1000;
export const CPA_VERSION_HEADER_KEYS = ['x-cpa-version'];
export const CPA_BUILD_DATE_HEADER_KEYS = ['x-cpa-build-date'];
export const CPA_SUPPORT_PLUGIN_HEADER_KEYS = ['x-cpa-support-plugin'];
export const VERSION_HEADER_KEYS = [...CPA_VERSION_HEADER_KEYS, 'x-server-version'];
export const BUILD_DATE_HEADER_KEYS = [...CPA_BUILD_DATE_HEADER_KEYS, 'x-server-build-date'];

// 日志相关
export const LOGS_TIMEOUT_MS = 60 * 1000;

// 认证文件分页
export const MAX_AUTH_FILE_SIZE = 10 * 1024 * 1024;

// 本地存储键名
export const STORAGE_KEY_AUTH = 'cli-proxy-auth';
export const STORAGE_KEY_THEME = 'cli-proxy-theme';
export const STORAGE_KEY_LANGUAGE = 'cli-proxy-language';

// 语言配置
export const LANGUAGE_ORDER = defineLanguageOrder(['zh-CN', 'zh-TW', 'en', 'ru'] as const);
export const LANGUAGE_LABEL_KEYS: Record<Language, string> = {
  'zh-CN': 'language.chinese',
  'zh-TW': 'language.chinese_tw',
  en: 'language.english',
  ru: 'language.russian',
};
export const SUPPORTED_LANGUAGES = LANGUAGE_ORDER;

// 通知持续时间
export const NOTIFICATION_DURATION_MS = 3000;
