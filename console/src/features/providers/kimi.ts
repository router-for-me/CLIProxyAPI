import type { Config, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { SponsorProviderRaw } from './types';

export const KIMI_PROVIDER_NAME = 'kimi';
export const KIMI_DISPLAY_NAME = 'Kimi';
export const KIMI_LEGACY_OPENAI_BASE_URL = 'https://api.moonshot.ai';
export const KIMI_DOMESTIC_BASE_URL = 'https://api.moonshot.cn';
export const KIMI_OPENAI_BASE_URL = `${KIMI_LEGACY_OPENAI_BASE_URL}/v1`;
export const KIMI_DOMESTIC_OPENAI_BASE_URL = `${KIMI_DOMESTIC_BASE_URL}/v1`;
export const KIMI_ANTHROPIC_BASE_URL = `${KIMI_LEGACY_OPENAI_BASE_URL}/anthropic`;
export const KIMI_DOMESTIC_ANTHROPIC_BASE_URL = `${KIMI_DOMESTIC_BASE_URL}/anthropic`;
export const KIMI_CHINESE_AFFILIATE_URL = 'https://platform.kimi.com/?aff=cliproxyapi';
export const KIMI_INTERNATIONAL_AFFILIATE_URL = 'https://platform.kimi.ai/?aff=cliproxyapi';

export const KIMI_BASE_URL_OPTIONS = [
  {
    id: 'domestic',
    descriptionKey: 'domestic',
    baseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL,
    openaiBaseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL,
    codexBaseUrl: KIMI_DOMESTIC_OPENAI_BASE_URL,
    anthropicBaseUrl: KIMI_DOMESTIC_ANTHROPIC_BASE_URL,
    geminiBaseUrl: '',
  },
  {
    id: 'overseas',
    descriptionKey: 'overseas',
    baseUrl: KIMI_OPENAI_BASE_URL,
    openaiBaseUrl: KIMI_OPENAI_BASE_URL,
    codexBaseUrl: KIMI_OPENAI_BASE_URL,
    anthropicBaseUrl: KIMI_ANTHROPIC_BASE_URL,
    geminiBaseUrl: '',
  },
] as const;

export const KIMI_PROTOCOL_LABELS = ['openai', 'anthropic', 'codexResponses'] as const;

export const getKimiAffiliateUrl = (language: string | undefined | null): string =>
  language?.toLowerCase().startsWith('zh')
    ? KIMI_CHINESE_AFFILIATE_URL
    : KIMI_INTERNATIONAL_AFFILIATE_URL;

const normalizeText = (value: string | undefined | null): string =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const normalizeBaseUrl = (value: string | undefined | null): string =>
  normalizeText(value).replace(/\/+$/, '');

export const resolveKimiBaseUrl = (value: string | undefined | null): string => {
  const normalized = normalizeBaseUrl(value);
  const matched = KIMI_BASE_URL_OPTIONS.find(
    (option) =>
      normalized === normalizeBaseUrl(option.baseUrl) ||
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl) ||
      normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
  if (matched) return matched.baseUrl;
  if (normalized === normalizeBaseUrl(KIMI_LEGACY_OPENAI_BASE_URL)) {
    return KIMI_OPENAI_BASE_URL;
  }
  return KIMI_DOMESTIC_OPENAI_BASE_URL;
};

export const getKimiProtocolUrls = (value: string | undefined | null) => {
  const baseUrl = resolveKimiBaseUrl(value);
  const matched =
    KIMI_BASE_URL_OPTIONS.find(
      (option) => normalizeBaseUrl(option.baseUrl) === normalizeBaseUrl(baseUrl)
    ) ?? KIMI_BASE_URL_OPTIONS[0];
  return {
    anthropic: matched.anthropicBaseUrl,
    openai: matched.openaiBaseUrl,
    codex: matched.codexBaseUrl,
    gemini: '',
  };
};

export const isKimiOpenAIProvider = (config: OpenAIProviderConfig | undefined | null): boolean => {
  if (!config) return false;
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  return (
    KIMI_BASE_URL_OPTIONS.some((option) => baseUrl === normalizeBaseUrl(option.openaiBaseUrl)) ||
    baseUrl === normalizeBaseUrl(KIMI_LEGACY_OPENAI_BASE_URL) ||
    baseUrl === normalizeBaseUrl(KIMI_DOMESTIC_BASE_URL)
  );
};

export const isKimiClaudeProvider = (config: ProviderKeyConfig | undefined | null): boolean => {
  if (!config) return false;
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  return KIMI_BASE_URL_OPTIONS.some(
    (option) => baseUrl === normalizeBaseUrl(option.anthropicBaseUrl)
  );
};

export const isKimiCodexProvider = (config: ProviderKeyConfig | undefined | null): boolean => {
  if (!config) return false;
  const baseUrl = normalizeBaseUrl(config.baseUrl);
  return KIMI_BASE_URL_OPTIONS.some(
    (option) => baseUrl === normalizeBaseUrl(option.codexBaseUrl)
  );
};

export const buildKimiRaw = (config: Config | null | undefined): SponsorProviderRaw => ({
  openai: (config?.openaiCompatibility ?? [])
    .map((item, index) => ({ config: item, index: item.sourceIndex ?? index }))
    .filter((item) => isKimiOpenAIProvider(item.config)),
  claude: (config?.claudeApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isKimiClaudeProvider(item.config)),
  codex: (config?.codexApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isKimiCodexProvider(item.config)),
  gemini: [],
});
