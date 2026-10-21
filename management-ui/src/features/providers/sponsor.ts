import type { Config, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { SponsorProviderRaw } from './types';

export const APIKEY_FUN_PROVIDER_NAME = 'apikeyFun';
export const APIKEY_FUN_DISPLAY_NAME = 'APIKEY.FUN';
export const APIKEY_FUN_AFFILIATE_URL = 'https://apikey.fan/register?aff=AKCPA';
export const APIKEY_FUN_DASHBOARD_URL = 'https://apikey.fan/dashboard';
export const APIKEY_FUN_STANDARD_BASE_URL = 'https://api.apikey.fan';
export const APIKEY_FUN_DIRECT_BASE_URL = 'https://slb.apikey.fan';
export const APIKEY_FUN_OPENAI_BASE_URL = `${APIKEY_FUN_STANDARD_BASE_URL}/v1`;
export const APIKEY_FUN_CODEX_BASE_URL = APIKEY_FUN_OPENAI_BASE_URL;
export const APIKEY_FUN_ANTHROPIC_BASE_URL = APIKEY_FUN_STANDARD_BASE_URL;
export const APIKEY_FUN_GEMINI_BASE_URL = APIKEY_FUN_STANDARD_BASE_URL;
export const APIKEY_FUN_USAGE_PATH = '/v1/usage';

export const APIKEY_FUN_BASE_URL_OPTIONS = [
  {
    id: 'standard',
    baseUrl: APIKEY_FUN_STANDARD_BASE_URL,
    openaiBaseUrl: APIKEY_FUN_OPENAI_BASE_URL,
    codexBaseUrl: APIKEY_FUN_CODEX_BASE_URL,
    anthropicBaseUrl: APIKEY_FUN_ANTHROPIC_BASE_URL,
    geminiBaseUrl: APIKEY_FUN_GEMINI_BASE_URL,
  },
  {
    id: 'direct',
    baseUrl: APIKEY_FUN_DIRECT_BASE_URL,
    openaiBaseUrl: `${APIKEY_FUN_DIRECT_BASE_URL}/v1`,
    codexBaseUrl: `${APIKEY_FUN_DIRECT_BASE_URL}/v1`,
    anthropicBaseUrl: APIKEY_FUN_DIRECT_BASE_URL,
    geminiBaseUrl: APIKEY_FUN_DIRECT_BASE_URL,
  },
] as const;

export const APIKEY_FUN_PROTOCOLS = ['anthropic', 'openai', 'codexResponses'] as const;

const normalizeText = (value: string | undefined | null): string =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const normalizeBaseUrl = (value: string | undefined | null): string =>
  normalizeText(value).replace(/\/+$/, '');

export const resolveApiKeyFunBaseUrl = (value: string | undefined | null): string => {
  const normalized = normalizeBaseUrl(value);
  const matched = APIKEY_FUN_BASE_URL_OPTIONS.find(
    (option) =>
      normalized === normalizeBaseUrl(option.baseUrl) ||
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl) ||
      normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
  return matched?.baseUrl ?? APIKEY_FUN_STANDARD_BASE_URL;
};

export const getApiKeyFunProtocolUrls = (value: string | undefined | null) => {
  const baseUrl = resolveApiKeyFunBaseUrl(value);
  const matched =
    APIKEY_FUN_BASE_URL_OPTIONS.find(
      (option) => normalizeBaseUrl(option.baseUrl) === normalizeBaseUrl(baseUrl)
    ) ?? APIKEY_FUN_BASE_URL_OPTIONS[0];
  return {
    anthropic: matched.anthropicBaseUrl,
    openai: matched.openaiBaseUrl,
    codex: matched.codexBaseUrl,
    gemini: matched.geminiBaseUrl,
  };
};

const buildApiKeyFunUsageEndpoint = (baseUrl: string): string =>
  `${baseUrl.replace(/\/+$/, '')}${APIKEY_FUN_USAGE_PATH}`;

export const getApiKeyFunUsageEndpoints = (value: string | undefined | null): string[] => {
  const baseUrl = resolveApiKeyFunBaseUrl(value);
  const primary = buildApiKeyFunUsageEndpoint(baseUrl);
  const standard = buildApiKeyFunUsageEndpoint(APIKEY_FUN_STANDARD_BASE_URL);
  return primary === standard ? [primary] : [primary, standard];
};

export interface ApiKeyFunUsageSummary {
  isValid: boolean;
  status?: string;
  mode?: string;
  remaining: number | string | null;
  unit: string;
  limit: number | string | null;
  used: number | string | null;
}

const normalizeUsageAmount = (value: unknown): number | string | null => {
  if (typeof value === 'number') {
    return Number.isFinite(value) ? value : null;
  }
  if (typeof value === 'string') {
    const trimmed = value.trim();
    return trimmed ? trimmed : null;
  }
  return null;
};

const normalizeString = (value: unknown): string | undefined =>
  typeof value === 'string' && value.trim() ? value.trim() : undefined;

const normalizeBoolean = (value: unknown, fallback: boolean): boolean => {
  if (typeof value === 'boolean') return value;
  if (typeof value === 'string') {
    const normalized = value.trim().toLowerCase();
    if (['true', '1', 'yes', 'active'].includes(normalized)) return true;
    if (['false', '0', 'no', 'inactive', 'disabled'].includes(normalized)) return false;
  }
  return fallback;
};

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

export const normalizeApiKeyFunUsagePayload = (payload: unknown): ApiKeyFunUsageSummary | null => {
  if (!isRecord(payload)) return null;

  const quota = isRecord(payload.quota) ? payload.quota : {};
  const remaining = normalizeUsageAmount(payload.remaining ?? quota.remaining ?? payload.balance);
  const unit = normalizeString(payload.unit ?? quota.unit) ?? 'USD';
  const limit = normalizeUsageAmount(quota.limit);
  const used = normalizeUsageAmount(quota.used);
  const status = normalizeString(payload.status);
  const mode = normalizeString(payload.mode);
  const isValid = normalizeBoolean(payload.is_active ?? payload.isValid, true);

  if (remaining === null && limit === null && used === null && !status && !mode) {
    return null;
  }

  return {
    isValid,
    status,
    mode,
    remaining,
    unit,
    limit,
    used,
  };
};

const matchesApiKeyFunOpenAIBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return APIKEY_FUN_BASE_URL_OPTIONS.some(
    (option) =>
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl)
  );
};

const matchesApiKeyFunAnthropicBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return APIKEY_FUN_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
};

export const isApiKeyFunOpenAIProvider = (
  config: OpenAIProviderConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesApiKeyFunOpenAIBaseUrl(config.baseUrl);
};

export const isApiKeyFunClaudeProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesApiKeyFunAnthropicBaseUrl(config.baseUrl);
};

export const isApiKeyFunCodexProvider = (config: ProviderKeyConfig | undefined | null): boolean => {
  if (!config) return false;
  return matchesApiKeyFunOpenAIBaseUrl(config.baseUrl);
};

export const buildApiKeyFunRaw = (config: Config | null | undefined): SponsorProviderRaw => ({
  openai: (config?.openaiCompatibility ?? [])
    .map((item, index) => ({ config: item, index: item.sourceIndex ?? index }))
    .filter((item) => isApiKeyFunOpenAIProvider(item.config)),
  claude: (config?.claudeApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isApiKeyFunClaudeProvider(item.config)),
  codex: (config?.codexApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isApiKeyFunCodexProvider(item.config)),
  gemini: [],
});

export const hasApiKeyFunConfig = (config: Config | null | undefined): boolean => {
  const raw = buildApiKeyFunRaw(config);
  return raw.openai.length > 0 || raw.claude.length > 0 || raw.codex.length > 0;
};
