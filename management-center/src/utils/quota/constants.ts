/**
 * Quota constants for API URLs, headers, and theme colors.
 */

import type { TypeColorSet } from '@/types';

// Theme colors for type badges — 与 authFiles/constants.ts 保持同步
export const TYPE_COLORS: Record<string, TypeColorSet> = {
  qwen: {
    light: { bg: '#ede5fd', text: '#5530c7' },
    dark: { bg: '#36208a', text: '#b5a3f0' },
  },
  gemini: {
    light: { bg: '#e3f2fd', text: '#1565c0' },
    dark: { bg: '#0d47a1', text: '#64b5f6' },
  },
  aistudio: {
    light: { bg: '#f0f2f5', text: '#2f343c' },
    dark: { bg: '#373c42', text: '#cfd3db' },
  },
  claude: {
    light: { bg: '#fbece4', text: '#c05621' },
    dark: { bg: '#5e2c14', text: '#e8a882' },
  },
  codex: {
    light: { bg: '#eae7ff', text: '#3538d4' },
    dark: { bg: '#262395', text: '#b5b0ff' },
  },
  devin: {
    light: { bg: '#e8f4ff', text: '#155e9b' },
    dark: { bg: '#123b5d', text: '#8dc9f5' },
  },
  meta: {
    light: { bg: '#e3f2fd', text: '#1565c0' },
    dark: { bg: '#0d47a1', text: '#64b5f6' },
  },
  kimi: {
    light: { bg: '#dce8ff', text: '#0560cf' },
    dark: { bg: '#003880', text: '#70b5ff' },
  },
  antigravity: {
    light: { bg: '#e0f7fa', text: '#006064' },
    dark: { bg: '#004d40', text: '#80deea' },
  },
  xai: {
    light: { bg: '#f3f4f6', text: '#111827', border: '1px solid #d1d5db' },
    dark: { bg: '#111827', text: '#f9fafb', border: '1px solid #374151' },
  },
  iflow: {
    light: { bg: '#f5e3fc', text: '#9025c8' },
    dark: { bg: '#521490', text: '#d49cf5' },
  },
  vertex: {
    light: { bg: '#e4edfd', text: '#2b5fbc' },
    dark: { bg: '#1a3d80', text: '#89b3f7' },
  },
  empty: {
    light: { bg: '#f5f5f5', text: '#616161' },
    dark: { bg: '#424242', text: '#bdbdbd' },
  },
  unknown: {
    light: { bg: '#f0f0f0', text: '#666666', border: '1px dashed #999999' },
    dark: { bg: '#3a3a3a', text: '#aaaaaa', border: '1px dashed #666666' },
  },
};

// Antigravity API configuration
export const ANTIGRAVITY_QUOTA_URLS = [
  'https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary',
  'https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary',
  'https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary',
];

export const ANTIGRAVITY_CODE_ASSIST_URL =
  'https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist';

export const ANTIGRAVITY_CLI_VERSION = '1.0.13';
export const ANTIGRAVITY_CLIENT_NAME = 'aidev_client';
export const ANTIGRAVITY_CLIENT_PLATFORM = {
  osType: 'darwin',
  arch: 'arm64',
} as const;

type AntigravityUserAgentOptions = {
  version?: string;
  clientName?: string;
  osType?: string;
  arch?: string;
};

export const buildAntigravityUserAgent = ({
  version = ANTIGRAVITY_CLI_VERSION,
  clientName = ANTIGRAVITY_CLIENT_NAME,
  osType = ANTIGRAVITY_CLIENT_PLATFORM.osType,
  arch = ANTIGRAVITY_CLIENT_PLATFORM.arch,
}: AntigravityUserAgentOptions = {}) =>
  `antigravity/cli/${version} (${clientName}; os_type=${osType}; arch=${arch})`;

export const ANTIGRAVITY_USER_AGENT = buildAntigravityUserAgent();

export const ANTIGRAVITY_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'User-Agent': ANTIGRAVITY_USER_AGENT,
};

// Claude API configuration
export const CLAUDE_PROFILE_URL = 'https://api.anthropic.com/api/oauth/profile';

export const CLAUDE_USAGE_URL = 'https://api.anthropic.com/api/oauth/usage';

export const CLAUDE_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'anthropic-beta': 'oauth-2025-04-20',
};

export const CLAUDE_USAGE_WINDOW_KEYS = [
  { key: 'five_hour', id: 'five-hour', labelKey: 'claude_quota.five_hour' },
  { key: 'seven_day', id: 'seven-day', labelKey: 'claude_quota.seven_day' },
  {
    key: 'seven_day_oauth_apps',
    id: 'seven-day-oauth-apps',
    labelKey: 'claude_quota.seven_day_oauth_apps',
  },
  { key: 'seven_day_opus', id: 'seven-day-opus', labelKey: 'claude_quota.seven_day_opus' },
  { key: 'seven_day_sonnet', id: 'seven-day-sonnet', labelKey: 'claude_quota.seven_day_sonnet' },
  { key: 'seven_day_cowork', id: 'seven-day-cowork', labelKey: 'claude_quota.seven_day_cowork' },
  { key: 'iguana_necktie', id: 'seven-day-fable', labelKey: 'claude_quota.seven_day_fable' },
] as const;

// Codex API configuration
export const CODEX_USAGE_URL = 'https://chatgpt.com/backend-api/wham/usage';
export const CODEX_SUBSCRIPTION_URL = 'https://chatgpt.com/backend-api/subscriptions';
export const CODEX_RATE_LIMIT_RESET_CREDITS_URL =
  'https://chatgpt.com/backend-api/wham/rate-limit-reset-credits';
export const CODEX_RATE_LIMIT_RESET_CREDITS_CONSUME_URL =
  'https://chatgpt.com/backend-api/wham/rate-limit-reset-credits/consume';

export const CODEX_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'Content-Type': 'application/json',
  'User-Agent': 'codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)',
};

// Kimi API configuration
export const KIMI_USAGE_URL = 'https://api.kimi.com/coding/v1/usages';

export const KIMI_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
};

// xAI/Grok API configuration
export const XAI_BILLING_WEEKLY_URL = 'https://cli-chat-proxy.grok.com/v1/billing?format=credits';
export const XAI_BILLING_MONTHLY_URL = 'https://cli-chat-proxy.grok.com/v1/billing';
export const XAI_API_ME_URL = 'https://api.x.ai/v1/me';
export const XAI_API_CHAT_URL = 'https://api.x.ai/v1/chat/completions';
export const XAI_PAID_HEALTH_MODEL = 'grok-4.5';
export const XAI_GROK_CLIENT_VERSION = '0.2.91';
export const XAI_GROK_USER_AGENT = 'grok-pager/0.2.91 grok-shell/0.2.91 (macos; aarch64)';

export const XAI_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  'x-xai-token-auth': 'xai-grok-cli',
  'x-grok-client-version': XAI_GROK_CLIENT_VERSION,
  accept: '*/*',
  'user-agent': XAI_GROK_USER_AGENT,
};

export const XAI_API_REQUEST_HEADERS = {
  Authorization: 'Bearer $TOKEN$',
  accept: 'application/json',
};
