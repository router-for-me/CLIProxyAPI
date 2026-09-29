import type { Config, GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { SponsorProviderRaw } from './types';

export const QINIU_CLOUD_PROVIDER_NAME = 'qiniuCloud';
export const QINIU_CLOUD_DISPLAY_NAME = '七牛云';
export const QINIU_CLOUD_AFFILIATE_URL = 'https://s.qiniu.com/miI73q';
export const QINIU_CLOUD_DOMESTIC_BASE_URL = 'https://api.qnaigc.com';
export const QINIU_CLOUD_OVERSEAS_BASE_URL = 'https://api.modelink.ai';

const openAIBaseUrl = (baseUrl: string): string => `${baseUrl}/v1`;

export const QINIU_CLOUD_BASE_URL_OPTIONS = [
  {
    id: 'domestic',
    descriptionKey: 'domestic',
    baseUrl: QINIU_CLOUD_DOMESTIC_BASE_URL,
    openaiBaseUrl: openAIBaseUrl(QINIU_CLOUD_DOMESTIC_BASE_URL),
    codexBaseUrl: openAIBaseUrl(QINIU_CLOUD_DOMESTIC_BASE_URL),
    anthropicBaseUrl: QINIU_CLOUD_DOMESTIC_BASE_URL,
    geminiBaseUrl: QINIU_CLOUD_DOMESTIC_BASE_URL,
  },
  {
    id: 'overseas',
    descriptionKey: 'overseas',
    baseUrl: QINIU_CLOUD_OVERSEAS_BASE_URL,
    openaiBaseUrl: openAIBaseUrl(QINIU_CLOUD_OVERSEAS_BASE_URL),
    codexBaseUrl: openAIBaseUrl(QINIU_CLOUD_OVERSEAS_BASE_URL),
    anthropicBaseUrl: QINIU_CLOUD_OVERSEAS_BASE_URL,
    geminiBaseUrl: QINIU_CLOUD_OVERSEAS_BASE_URL,
  },
] as const;

export const QINIU_CLOUD_PROTOCOL_LABELS = [
  'openai',
  'anthropic',
  'gemini',
  'codexResponses',
] as const;

const normalizeText = (value: string | undefined | null): string =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const normalizeBaseUrl = (value: string | undefined | null): string =>
  normalizeText(value).replace(/\/+$/, '');

export const resolveQiniuCloudBaseUrl = (value: string | undefined | null): string => {
  const normalized = normalizeBaseUrl(value);
  const matched = QINIU_CLOUD_BASE_URL_OPTIONS.find(
    (option) =>
      normalized === normalizeBaseUrl(option.baseUrl) ||
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl) ||
      normalized === normalizeBaseUrl(option.anthropicBaseUrl) ||
      normalized === normalizeBaseUrl(option.geminiBaseUrl)
  );
  return matched?.baseUrl ?? QINIU_CLOUD_DOMESTIC_BASE_URL;
};

export const getQiniuCloudProtocolUrls = (value: string | undefined | null) => {
  const baseUrl = resolveQiniuCloudBaseUrl(value);
  const matched =
    QINIU_CLOUD_BASE_URL_OPTIONS.find(
      (option) => normalizeBaseUrl(option.baseUrl) === normalizeBaseUrl(baseUrl)
    ) ?? QINIU_CLOUD_BASE_URL_OPTIONS[0];
  return {
    anthropic: matched.anthropicBaseUrl,
    openai: matched.openaiBaseUrl,
    codex: matched.codexBaseUrl,
    gemini: matched.geminiBaseUrl,
  };
};

const matchesQiniuCloudOpenAIBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return QINIU_CLOUD_BASE_URL_OPTIONS.some(
    (option) =>
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl)
  );
};

const matchesQiniuCloudAnthropicBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return QINIU_CLOUD_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
};

const matchesQiniuCloudGeminiBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return QINIU_CLOUD_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.geminiBaseUrl)
  );
};

export const isQiniuCloudOpenAIProvider = (
  config: OpenAIProviderConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesQiniuCloudOpenAIBaseUrl(config.baseUrl);
};

export const isQiniuCloudClaudeProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesQiniuCloudAnthropicBaseUrl(config.baseUrl);
};

export const isQiniuCloudCodexProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesQiniuCloudOpenAIBaseUrl(config.baseUrl);
};

export const isQiniuCloudGeminiProvider = (
  config: GeminiKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesQiniuCloudGeminiBaseUrl(config.baseUrl);
};

export const buildQiniuCloudRaw = (config: Config | null | undefined): SponsorProviderRaw => ({
  openai: (config?.openaiCompatibility ?? [])
    .map((item, index) => ({ config: item, index: item.sourceIndex ?? index }))
    .filter((item) => isQiniuCloudOpenAIProvider(item.config)),
  claude: (config?.claudeApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isQiniuCloudClaudeProvider(item.config)),
  codex: (config?.codexApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isQiniuCloudCodexProvider(item.config)),
  gemini: (config?.geminiApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isQiniuCloudGeminiProvider(item.config)),
});
