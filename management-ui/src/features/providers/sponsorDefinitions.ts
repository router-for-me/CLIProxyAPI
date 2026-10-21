import {
  APIKEY_FUN_AFFILIATE_URL,
  APIKEY_FUN_BASE_URL_OPTIONS,
  APIKEY_FUN_DASHBOARD_URL,
  APIKEY_FUN_DISPLAY_NAME,
  APIKEY_FUN_PROTOCOLS,
  APIKEY_FUN_PROVIDER_NAME,
  getApiKeyFunProtocolUrls,
  resolveApiKeyFunBaseUrl,
} from './sponsor';
import {
  FENNO_AI_AFFILIATE_URL,
  FENNO_AI_BASE_URL_OPTIONS,
  FENNO_AI_DISPLAY_NAME,
  FENNO_AI_PROTOCOL_LABELS,
  FENNO_AI_PROVIDER_NAME,
  getFennoAIProtocolUrls,
  resolveFennoAIBaseUrl,
} from './fennoAI';
import {
  QINIU_CLOUD_AFFILIATE_URL,
  QINIU_CLOUD_BASE_URL_OPTIONS,
  QINIU_CLOUD_DISPLAY_NAME,
  QINIU_CLOUD_PROTOCOL_LABELS,
  QINIU_CLOUD_PROVIDER_NAME,
  getQiniuCloudProtocolUrls,
  resolveQiniuCloudBaseUrl,
} from './qiniuCloud';
import {
  KIMI_BASE_URL_OPTIONS,
  KIMI_DISPLAY_NAME,
  KIMI_PROTOCOL_LABELS,
  KIMI_PROVIDER_NAME,
  getKimiProtocolUrls,
  resolveKimiBaseUrl,
} from './kimi';
import type {
  ProviderBrand,
  SponsorProtocol,
  SponsorProviderBrand,
  SponsorProviderRaw,
} from './types';

export interface SponsorProtocolUrls {
  anthropic: string;
  openai: string;
  codex: string;
  gemini: string;
}

export interface SponsorBaseUrlOption {
  id: string;
  descriptionKey?: string;
  baseUrl: string;
  openaiBaseUrl: string;
  codexBaseUrl: string;
  anthropicBaseUrl: string;
  geminiBaseUrl: string;
}

export interface SponsorProviderDefinition {
  brand: SponsorProviderBrand;
  displayName: string;
  providerName: string;
  affiliateUrl?: string;
  dashboardUrl?: string;
  protocols: readonly SponsorProtocol[];
  protocolLabels: readonly string[];
  defaultProtocol: SponsorProtocol;
  baseUrlOptions: readonly SponsorBaseUrlOption[];
  supportsUsageCheck: boolean;
  resolveBaseUrl: (value: string | undefined | null) => string;
  getProtocolUrls: (value: string | undefined | null) => SponsorProtocolUrls;
}

const SPONSOR_DEFINITIONS: Record<SponsorProviderBrand, SponsorProviderDefinition> = {
  apikeyFun: {
    brand: 'apikeyFun',
    displayName: APIKEY_FUN_DISPLAY_NAME,
    providerName: APIKEY_FUN_PROVIDER_NAME,
    affiliateUrl: APIKEY_FUN_AFFILIATE_URL,
    dashboardUrl: APIKEY_FUN_DASHBOARD_URL,
    protocols: ['codex', 'claude', 'openai'],
    protocolLabels: APIKEY_FUN_PROTOCOLS,
    defaultProtocol: 'codex',
    baseUrlOptions: APIKEY_FUN_BASE_URL_OPTIONS,
    supportsUsageCheck: true,
    resolveBaseUrl: resolveApiKeyFunBaseUrl,
    getProtocolUrls: getApiKeyFunProtocolUrls,
  },
  fennoAI: {
    brand: 'fennoAI',
    displayName: FENNO_AI_DISPLAY_NAME,
    providerName: FENNO_AI_PROVIDER_NAME,
    affiliateUrl: FENNO_AI_AFFILIATE_URL,
    protocols: ['codex', 'claude'],
    protocolLabels: FENNO_AI_PROTOCOL_LABELS,
    defaultProtocol: 'codex',
    baseUrlOptions: FENNO_AI_BASE_URL_OPTIONS,
    supportsUsageCheck: false,
    resolveBaseUrl: resolveFennoAIBaseUrl,
    getProtocolUrls: getFennoAIProtocolUrls,
  },
  qiniuCloud: {
    brand: 'qiniuCloud',
    displayName: QINIU_CLOUD_DISPLAY_NAME,
    providerName: QINIU_CLOUD_PROVIDER_NAME,
    affiliateUrl: QINIU_CLOUD_AFFILIATE_URL,
    protocols: ['openai', 'claude', 'gemini', 'codex'],
    protocolLabels: QINIU_CLOUD_PROTOCOL_LABELS,
    defaultProtocol: 'openai',
    baseUrlOptions: QINIU_CLOUD_BASE_URL_OPTIONS,
    supportsUsageCheck: false,
    resolveBaseUrl: resolveQiniuCloudBaseUrl,
    getProtocolUrls: getQiniuCloudProtocolUrls,
  },
  kimi: {
    brand: 'kimi',
    displayName: KIMI_DISPLAY_NAME,
    providerName: KIMI_PROVIDER_NAME,
    protocols: ['openai', 'claude', 'codex'],
    protocolLabels: KIMI_PROTOCOL_LABELS,
    defaultProtocol: 'openai',
    baseUrlOptions: KIMI_BASE_URL_OPTIONS,
    supportsUsageCheck: false,
    resolveBaseUrl: resolveKimiBaseUrl,
    getProtocolUrls: getKimiProtocolUrls,
  },
};

export const isMultiProtocolSponsorBrand = (brand: ProviderBrand): brand is SponsorProviderBrand =>
  brand === 'apikeyFun' ||
  brand === 'fennoAI' ||
  brand === 'qiniuCloud' ||
  brand === 'kimi';

export type SponsorAggregationConflict = 'multiple-configs' | 'multiple-openai-keys';

export const getSponsorAggregationConflict = (
  raw: SponsorProviderRaw | null | undefined
): SponsorAggregationConflict | null => {
  if (!raw) return null;
  if (
    raw.openai.length > 1 ||
    raw.claude.length > 1 ||
    raw.codex.length > 1 ||
    raw.gemini.length > 1
  ) {
    return 'multiple-configs';
  }

  const openAIKeyCount = raw.openai.reduce(
    (count, item) =>
      count + (item.config.apiKeyEntries ?? []).filter((entry) => entry.apiKey?.trim()).length,
    0
  );
  return openAIKeyCount > 1 ? 'multiple-openai-keys' : null;
};

export const getSponsorProviderDefinition = (
  brand: SponsorProviderBrand
): SponsorProviderDefinition => SPONSOR_DEFINITIONS[brand];

export const sponsorProtocolI18nKey = (
  protocol: SponsorProtocol
): 'openai' | 'codexResponses' | 'anthropic' | 'gemini' => {
  if (protocol === 'claude') return 'anthropic';
  if (protocol === 'codex') return 'codexResponses';
  return protocol;
};

export const sponsorProtocolModelI18nKey = (
  protocol: SponsorProtocol
): 'openai' | 'codex' | 'anthropic' | 'gemini' => {
  if (protocol === 'claude') return 'anthropic';
  return protocol;
};

export const discoveryBrandForSponsorProtocol = (protocol: SponsorProtocol): ProviderBrand =>
  protocol === 'openai' ? 'openaiCompatibility' : protocol;

export const sponsorProtocolUrl = (
  urls: SponsorProtocolUrls,
  protocol: SponsorProtocol
): string => {
  if (protocol === 'claude') return urls.anthropic;
  if (protocol === 'codex') return urls.codex;
  if (protocol === 'gemini') return urls.gemini;
  return urls.openai;
};
