/**
 * 常量定义
 * 从原项目 src/utils/constants.js 迁移
 */

// 缓存过期时间（毫秒）
export const CACHE_EXPIRY_MS = 30 * 1000; // 与基线保持一致，减少管理端压力

// 网络与版本信息
export const DEFAULT_API_PORT = 8317;
export const MANAGEMENT_API_PREFIX = '/v0/management';
export const REQUEST_TIMEOUT_MS = 30 * 1000;
export const VERSION_HEADER_KEYS = ['x-cpa-version', 'x-server-version'];
export const BUILD_DATE_HEADER_KEYS = ['x-cpa-build-date', 'x-server-build-date'];
export const STATUS_UPDATE_INTERVAL_MS = 1000;
export const LOG_REFRESH_DELAY_MS = 500;

// 日志相关
export const MAX_LOG_LINES = 2000;
export const LOG_FETCH_LIMIT = 2500;
export const LOGS_TIMEOUT_MS = 60 * 1000;

// 认证文件分页
export const DEFAULT_AUTH_FILES_PAGE_SIZE = 20;
export const MIN_AUTH_FILES_PAGE_SIZE = 10;
export const MAX_AUTH_FILES_PAGE_SIZE = 100;
export const MAX_AUTH_FILE_SIZE = 10 * 1024 * 1024;

// 本地存储键名
export const STORAGE_KEY_AUTH = 'cli-proxy-auth';
export const STORAGE_KEY_THEME = 'cli-proxy-theme';
export const STORAGE_KEY_LANGUAGE = 'cli-proxy-language';
export const STORAGE_KEY_SIDEBAR = 'cli-proxy-sidebar-collapsed';
export const STORAGE_KEY_AUTH_FILES_PAGE_SIZE = 'cli-proxy-auth-files-page-size';

// 通知持续时间
export const NOTIFICATION_DURATION_MS = 3000;

// OAuth 卡片 ID 列表
export const OAUTH_CARD_IDS = [
  'codex-oauth-card',
  'anthropic-oauth-card',
  'antigravity-oauth-card',
  'gemini-cli-oauth-card',
  'qwen-oauth-card'
];
export const OAUTH_PROVIDERS = {
  CODEX: 'codex',
  ANTHROPIC: 'anthropic',
  ANTIGRAVITY: 'antigravity',
  GEMINI_CLI: 'gemini-cli',
  QWEN: 'qwen'
} as const;

// API 端点
export const API_ENDPOINTS = {
  CONFIG: '/config',
  LOGIN: '/login',
  API_KEYS: '/api-keys',
  PROVIDERS: '/providers',
  AUTH_FILES: '/auth-files',
  OAUTH: '/oauth',
  USAGE: '/usage',
  LOGS: '/logs'
} as const;
