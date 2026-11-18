import type { Config, GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import type { SponsorProviderRaw } from './types';

export const AI_CODE_MIRROR_PROVIDER_NAME = 'aiCodeMirror';
export const AI_CODE_MIRROR_DISPLAY_NAME = 'AIcodemirror';
export const AI_CODE_MIRROR_AFFILIATE_URL =
  'https://www.aicodemirror.ai/register?invitecode=YNE3NQ';
export const AI_CODE_MIRROR_GLOBAL_BASE_URL = 'https://api.aicodemirror.ai';
export const AI_CODE_MIRROR_DOMESTIC_BASE_URL = 'https://api.claudecode.net.cn';

const openAIBaseUrl = (baseUrl: string): string => `${baseUrl}/v1`;

export const AI_CODE_MIRROR_BASE_URL_OPTIONS = [
  {
    id: 'aiCodeMirrorGlobal',
    descriptionKey: 'aiCodeMirrorGlobal',
    baseUrl: AI_CODE_MIRROR_GLOBAL_BASE_URL,
    openaiBaseUrl: openAIBaseUrl(AI_CODE_MIRROR_GLOBAL_BASE_URL),
    codexBaseUrl: openAIBaseUrl(AI_CODE_MIRROR_GLOBAL_BASE_URL),
    anthropicBaseUrl: AI_CODE_MIRROR_GLOBAL_BASE_URL,
    geminiBaseUrl: AI_CODE_MIRROR_GLOBAL_BASE_URL,
  },
  {
    id: 'aiCodeMirrorDomestic',
    descriptionKey: 'aiCodeMirrorDomestic',
    baseUrl: AI_CODE_MIRROR_DOMESTIC_BASE_URL,
    openaiBaseUrl: openAIBaseUrl(AI_CODE_MIRROR_DOMESTIC_BASE_URL),
    codexBaseUrl: openAIBaseUrl(AI_CODE_MIRROR_DOMESTIC_BASE_URL),
    anthropicBaseUrl: AI_CODE_MIRROR_DOMESTIC_BASE_URL,
    geminiBaseUrl: AI_CODE_MIRROR_DOMESTIC_BASE_URL,
  },
] as const;

export const AI_CODE_MIRROR_PROTOCOL_LABELS = [
  'openai',
  'anthropic',
  'gemini',
  'codexResponses',
] as const;

const normalizeBaseUrl = (value: string | undefined | null): string =>
  String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/\/+$/, '');

export const resolveAiCodeMirrorBaseUrl = (value: string | undefined | null): string => {
  const normalized = normalizeBaseUrl(value);
  const matched = AI_CODE_MIRROR_BASE_URL_OPTIONS.find(
    (option) =>
      normalized === normalizeBaseUrl(option.baseUrl) ||
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl) ||
      normalized === normalizeBaseUrl(option.anthropicBaseUrl) ||
      normalized === normalizeBaseUrl(option.geminiBaseUrl)
  );
  return matched?.baseUrl ?? AI_CODE_MIRROR_GLOBAL_BASE_URL;
};

export const getAiCodeMirrorProtocolUrls = (value: string | undefined | null) => {
  const baseUrl = resolveAiCodeMirrorBaseUrl(value);
  const matched =
    AI_CODE_MIRROR_BASE_URL_OPTIONS.find(
      (option) => normalizeBaseUrl(option.baseUrl) === normalizeBaseUrl(baseUrl)
    ) ?? AI_CODE_MIRROR_BASE_URL_OPTIONS[0];
  return {
    anthropic: matched.anthropicBaseUrl,
    openai: matched.openaiBaseUrl,
    codex: matched.codexBaseUrl,
    gemini: matched.geminiBaseUrl,
  };
};

const matchesAiCodeMirrorOpenAIBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return AI_CODE_MIRROR_BASE_URL_OPTIONS.some(
    (option) =>
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl)
  );
};

const matchesAiCodeMirrorAnthropicBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return AI_CODE_MIRROR_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
};

const matchesAiCodeMirrorGeminiBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return AI_CODE_MIRROR_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.geminiBaseUrl)
  );
};

export const isAiCodeMirrorOpenAIProvider = (
  config: OpenAIProviderConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesAiCodeMirrorOpenAIBaseUrl(config.baseUrl);
};

export const isAiCodeMirrorClaudeProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesAiCodeMirrorAnthropicBaseUrl(config.baseUrl);
};

export const isAiCodeMirrorCodexProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesAiCodeMirrorOpenAIBaseUrl(config.baseUrl);
};

export const isAiCodeMirrorGeminiProvider = (
  config: GeminiKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesAiCodeMirrorGeminiBaseUrl(config.baseUrl);
};

export const buildAiCodeMirrorRaw = (config: Config | null | undefined): SponsorProviderRaw => ({
  openai: (config?.openaiCompatibility ?? [])
    .map((item, index) => ({ config: item, index: item.sourceIndex ?? index }))
    .filter((item) => isAiCodeMirrorOpenAIProvider(item.config)),
  claude: (config?.claudeApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isAiCodeMirrorClaudeProvider(item.config)),
  codex: (config?.codexApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isAiCodeMirrorCodexProvider(item.config)),
  gemini: (config?.geminiApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isAiCodeMirrorGeminiProvider(item.config)),
});
