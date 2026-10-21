import type { Config, ProviderKeyConfig } from '@/types';
import type { SponsorProviderRaw } from './types';

export const FENNO_AI_PROVIDER_NAME = 'fennoAI';
export const FENNO_AI_DISPLAY_NAME = 'FennoAI';
export const FENNO_AI_AFFILIATE_URL = 'https://api.fenno.ai/register?aff=DQFAMNB6CBLY';
export const FENNO_AI_BASE_URL = 'https://api.fenno.ai';
export const FENNO_AI_CODEX_BASE_URL = `${FENNO_AI_BASE_URL}/v1`;
export const FENNO_AI_ANTHROPIC_BASE_URL = FENNO_AI_BASE_URL;
export const FENNO_AI_OPENAI_BASE_URL = FENNO_AI_CODEX_BASE_URL;
export const FENNO_AI_GEMINI_BASE_URL = FENNO_AI_BASE_URL;

export const FENNO_AI_BASE_URL_OPTIONS = [
  {
    id: 'standard',
    baseUrl: FENNO_AI_BASE_URL,
    openaiBaseUrl: FENNO_AI_OPENAI_BASE_URL,
    codexBaseUrl: FENNO_AI_CODEX_BASE_URL,
    anthropicBaseUrl: FENNO_AI_ANTHROPIC_BASE_URL,
    geminiBaseUrl: FENNO_AI_GEMINI_BASE_URL,
  },
] as const;

export const FENNO_AI_PROTOCOL_LABELS = ['codexResponses', 'anthropic'] as const;

const normalizeText = (value: string | undefined | null): string =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const normalizeBaseUrl = (value: string | undefined | null): string =>
  normalizeText(value).replace(/\/+$/, '');

export const resolveFennoAIBaseUrl = (value: string | undefined | null): string => {
  const normalized = normalizeBaseUrl(value);
  const matched = FENNO_AI_BASE_URL_OPTIONS.find(
    (option) =>
      normalized === normalizeBaseUrl(option.baseUrl) ||
      normalized === normalizeBaseUrl(option.openaiBaseUrl) ||
      normalized === normalizeBaseUrl(option.codexBaseUrl) ||
      normalized === normalizeBaseUrl(option.anthropicBaseUrl) ||
      normalized === normalizeBaseUrl(option.geminiBaseUrl)
  );
  return matched?.baseUrl ?? FENNO_AI_BASE_URL;
};

export const getFennoAIProtocolUrls = (value: string | undefined | null) => {
  const baseUrl = resolveFennoAIBaseUrl(value);
  const matched =
    FENNO_AI_BASE_URL_OPTIONS.find(
      (option) => normalizeBaseUrl(option.baseUrl) === normalizeBaseUrl(baseUrl)
    ) ?? FENNO_AI_BASE_URL_OPTIONS[0];
  return {
    anthropic: matched.anthropicBaseUrl,
    openai: matched.openaiBaseUrl,
    codex: matched.codexBaseUrl,
    gemini: matched.geminiBaseUrl,
  };
};

const matchesFennoAICodexBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return FENNO_AI_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.codexBaseUrl)
  );
};

const matchesFennoAIAnthropicBaseUrl = (value: string | undefined | null): boolean => {
  const normalized = normalizeBaseUrl(value);
  return FENNO_AI_BASE_URL_OPTIONS.some(
    (option) => normalized === normalizeBaseUrl(option.anthropicBaseUrl)
  );
};

export const isFennoAIClaudeProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesFennoAIAnthropicBaseUrl(config.baseUrl);
};

export const isFennoAICodexProvider = (
  config: ProviderKeyConfig | undefined | null
): boolean => {
  if (!config) return false;
  return matchesFennoAICodexBaseUrl(config.baseUrl);
};

export const buildFennoAIRaw = (config: Config | null | undefined): SponsorProviderRaw => ({
  // FennoAI does not expose OpenAI in its protocol definition. Keep any
  // name-matching OpenAI compatibility entry in the generic provider group.
  openai: [],
  claude: (config?.claudeApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isFennoAIClaudeProvider(item.config)),
  codex: (config?.codexApiKeys ?? [])
    .map((item, index) => ({ config: item, index }))
    .filter((item) => isFennoAICodexProvider(item.config)),
  gemini: [],
});
