import type { GeminiKeyConfig, OpenAIProviderConfig, ProviderKeyConfig } from '@/types';
import { hasDisableAllModelsRule, stripDisableAllModelsRule } from '@/components/providers/utils';
import { maskApiKey } from '@/utils/format';
import {
  APIKEY_FUN_DISPLAY_NAME,
  APIKEY_FUN_PROTOCOLS,
  getApiKeyFunProtocolUrls,
  resolveApiKeyFunBaseUrl,
} from './sponsor';
import {
  FENNO_AI_DISPLAY_NAME,
  FENNO_AI_PROTOCOL_LABELS,
  getFennoAIProtocolUrls,
  resolveFennoAIBaseUrl,
} from './fennoAI';
import {
  QINIU_CLOUD_DISPLAY_NAME,
  QINIU_CLOUD_PROTOCOL_LABELS,
  getQiniuCloudProtocolUrls,
  resolveQiniuCloudBaseUrl,
} from './qiniuCloud';
import {
  KIMI_DISPLAY_NAME,
  KIMI_PROTOCOL_LABELS,
  getKimiProtocolUrls,
  resolveKimiBaseUrl,
} from './kimi';
import type {
  ProviderBrand,
  ProviderResource,
  ProviderResourceSelector,
  SponsorProviderBrand,
  SponsorProviderRaw,
} from './types';

const countHeaders = (headers?: Record<string, string>): number =>
  headers ? Object.keys(headers).length : 0;

const collectModelNames = (models?: Array<{ name?: string }>): string[] => {
  const seen = new Set<string>();
  (models ?? []).forEach((model) => {
    const name = (model?.name ?? '').trim();
    if (name) seen.add(name);
  });
  return Array.from(seen);
};

const normalizePriority = (priority?: number): number =>
  typeof priority === 'number' && Number.isFinite(priority) ? priority : 0;

const buildId = (brand: ProviderBrand, index: number, fragment: string) =>
  `${brand}:${index}:${fragment || 'item'}`;

const truncateForId = (value: string | undefined | null): string => {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) return '';
  if (trimmed.length <= 12) return trimmed;
  return trimmed.slice(0, 8);
};

function providerKeyToResource(
  brand: 'gemini' | 'interactions' | 'codex' | 'meta' | 'xai' | 'claude' | 'vertex',
  config: GeminiKeyConfig | ProviderKeyConfig,
  index: number
): ProviderResource {
  const apiKey = config.apiKey ?? '';
  const disabled = hasDisableAllModelsRule(config.excludedModels);
  const flags: ProviderResource['flags'] = {};
  if (brand === 'codex' || brand === 'xai') {
    flags.websockets = (config as ProviderKeyConfig).websockets === true;
  }
  if (brand === 'claude') {
    const claudeConfig = config as ProviderKeyConfig;
    flags.cloakEnabled = Boolean(claudeConfig.cloak?.mode?.trim());
    flags.claudeCodeCliProfile = claudeConfig.fingerprintProfile === 'claude-code-cli';
  }

  const selector: ProviderResourceSelector = {
    brand,
    apiKey,
    baseUrl: config.baseUrl,
    index,
  } as ProviderResourceSelector;

  return {
    id: buildId(brand, index, truncateForId(apiKey)),
    brand,
    originalIndex: index,
    name: typeof config.source?.group.name === 'string' ? config.source.group.name : null,
    identifier: maskApiKey(apiKey) || `#${index + 1}`,
    apiKeyPreview: apiKey ? maskApiKey(apiKey) : null,
    apiKey: apiKey || null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: config.proxyUrl ?? null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: stripDisableAllModelsRule(config.excludedModels).length,
    apiKeyEntryCount: 0,
    disabled,
    flags,
    selector,
    raw: config,
  };
}

export function geminiToResource(config: GeminiKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('gemini', config, index);
}

export function interactionsToResource(config: GeminiKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('interactions', config, index);
}

export function codexToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('codex', config, index);
}

export function metaToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('meta', config, index);
}

export function xaiToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('xai', config, index);
}

export function claudeToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('claude', config, index);
}

export function vertexToResource(config: ProviderKeyConfig, index: number): ProviderResource {
  return providerKeyToResource('vertex', config, index);
}

export function openaiToResource(config: OpenAIProviderConfig, index: number): ProviderResource {
  const sourceIndex = config.sourceIndex ?? index;
  const name = (config.name ?? '').trim();
  const firstEntry = config.apiKeyEntries?.[0];
  const previewApiKey = firstEntry?.apiKey ? maskApiKey(firstEntry.apiKey) : null;
  return {
    id: buildId('openaiCompatibility', sourceIndex, truncateForId(name) || `#${sourceIndex}`),
    brand: 'openaiCompatibility',
    originalIndex: sourceIndex,
    name: name || null,
    identifier: name || `#${sourceIndex + 1}`,
    apiKeyPreview: previewApiKey,
    apiKey: null,
    authIndex: config.authIndex ?? null,
    baseUrl: config.baseUrl ?? null,
    proxyUrl: null,
    prefix: config.prefix ?? null,
    modelCount: config.models?.length ?? 0,
    models: collectModelNames(config.models),
    priority: normalizePriority(config.priority),
    headerCount: countHeaders(config.headers),
    excludedModelCount: 0,
    apiKeyEntryCount: config.apiKeyEntries?.length ?? 0,
    disabled: config.disabled === true,
    flags: {},
    selector: { brand: 'openaiCompatibility', name, index: sourceIndex },
    raw: config,
  };
}

interface SponsorResourceOptions {
  displayName: string;
  protocolLabels: readonly string[];
  resolveBaseUrl: (value: string | undefined | null) => string;
  getProtocolUrls: (value: string | undefined | null) => {
    anthropic: string;
    openai: string;
    codex: string;
    gemini: string;
  };
}

function sponsorRawToResource(
  brand: SponsorProviderBrand,
  raw: SponsorProviderRaw,
  options: SponsorResourceOptions
): ProviderResource | null {
  if (
    raw.openai.length === 0 &&
    raw.claude.length === 0 &&
    raw.codex.length === 0 &&
    raw.gemini.length === 0
  ) {
    return null;
  }
  const openaiKeyCount = raw.openai.reduce(
    (count, item) => count + (item.config.apiKeyEntries?.length ?? 0),
    0
  );
  const codexKeyCount = raw.codex.length;
  const geminiKeyCount = raw.gemini.length;
  const firstOpenAIEntry = raw.openai
    .flatMap((item) => item.config.apiKeyEntries ?? [])
    .find((entry) => entry.apiKey?.trim());
  const firstCodex = raw.codex.find((item) => item.config.apiKey?.trim());
  const firstClaude = raw.claude.find((item) => item.config.apiKey?.trim());
  const firstGemini = raw.gemini.find((item) => item.config.apiKey?.trim());
  const apiKey =
    firstOpenAIEntry?.apiKey ??
    firstCodex?.config.apiKey ??
    firstClaude?.config.apiKey ??
    firstGemini?.config.apiKey ??
    '';
  const openaiDisabled =
    raw.openai.length > 0 && raw.openai.every((item) => item.config.disabled === true);
  const codexDisabled =
    raw.codex.length > 0 &&
    raw.codex.every((item) => hasDisableAllModelsRule(item.config.excludedModels));
  const claudeDisabled =
    raw.claude.length > 0 &&
    raw.claude.every((item) => hasDisableAllModelsRule(item.config.excludedModels));
  const geminiDisabled =
    raw.gemini.length > 0 &&
    raw.gemini.every((item) => hasDisableAllModelsRule(item.config.excludedModels));
  const enabledCount =
    (raw.openai.length > 0 && !openaiDisabled ? 1 : 0) +
    (raw.codex.length > 0 && !codexDisabled ? 1 : 0) +
    (raw.claude.length > 0 && !claudeDisabled ? 1 : 0) +
    (raw.gemini.length > 0 && !geminiDisabled ? 1 : 0);
  const allResourcesConfigured =
    raw.openai.length > 0 || raw.codex.length > 0 || raw.claude.length > 0 || raw.gemini.length > 0;
  const disabled = allResourcesConfigured && enabledCount === 0;
  const models = [
    ...raw.openai.flatMap((item) => collectModelNames(item.config.models)),
    ...raw.codex.flatMap((item) => collectModelNames(item.config.models)),
    ...raw.claude.flatMap((item) => collectModelNames(item.config.models)),
    ...raw.gemini.flatMap((item) => collectModelNames(item.config.models)),
  ];
  const uniqueModels = Array.from(new Set(models));
  const headerCount =
    raw.openai.reduce((count, item) => count + countHeaders(item.config.headers), 0) +
    raw.codex.reduce((count, item) => count + countHeaders(item.config.headers), 0) +
    raw.claude.reduce((count, item) => count + countHeaders(item.config.headers), 0) +
    raw.gemini.reduce((count, item) => count + countHeaders(item.config.headers), 0);
  const priority = Math.max(
    0,
    ...raw.openai.map((item) => normalizePriority(item.config.priority)),
    ...raw.codex.map((item) => normalizePriority(item.config.priority)),
    ...raw.claude.map((item) => normalizePriority(item.config.priority)),
    ...raw.gemini.map((item) => normalizePriority(item.config.priority))
  );
  const baseUrl = options.resolveBaseUrl(
    raw.openai[0]?.config.baseUrl ??
      raw.codex[0]?.config.baseUrl ??
      raw.claude[0]?.config.baseUrl ??
      raw.gemini[0]?.config.baseUrl
  );
  const protocolUrls = options.getProtocolUrls(baseUrl);

  return {
    id: buildId(brand, 0, 'sponsor'),
    brand,
    originalIndex: 0,
    name: options.displayName,
    identifier: options.displayName,
    apiKeyPreview: apiKey ? maskApiKey(apiKey) : null,
    apiKey: apiKey || null,
    authIndex: null,
    baseUrl: [protocolUrls.openai, protocolUrls.anthropic, protocolUrls.gemini]
      .filter(Boolean)
      .join(' / '),
    proxyUrl:
      firstOpenAIEntry?.proxyUrl ??
      raw.codex.find((item) => item.config.proxyUrl)?.config.proxyUrl ??
      raw.claude.find((item) => item.config.proxyUrl)?.config.proxyUrl ??
      raw.gemini.find((item) => item.config.proxyUrl)?.config.proxyUrl ??
      null,
    prefix:
      raw.openai[0]?.config.prefix ??
      raw.codex[0]?.config.prefix ??
      raw.claude[0]?.config.prefix ??
      raw.gemini[0]?.config.prefix ??
      null,
    modelCount: uniqueModels.length,
    models: uniqueModels,
    priority,
    headerCount,
    excludedModelCount:
      raw.codex.reduce(
        (count, item) => count + stripDisableAllModelsRule(item.config.excludedModels).length,
        0
      ) +
      raw.claude.reduce(
        (count, item) => count + stripDisableAllModelsRule(item.config.excludedModels).length,
        0
      ) +
      raw.gemini.reduce(
        (count, item) => count + stripDisableAllModelsRule(item.config.excludedModels).length,
        0
      ),
    apiKeyEntryCount: openaiKeyCount + codexKeyCount + raw.claude.length + geminiKeyCount,
    disabled,
    flags: {
      protocols: [...options.protocolLabels],
    },
    selector: {
      brand,
      openaiIndices: raw.openai.map((item) => item.index),
      claudeIndices: raw.claude.map((item) => item.index),
      codexIndices: raw.codex.map((item) => item.index),
      geminiIndices: raw.gemini.map((item) => item.index),
    } as ProviderResourceSelector,
    raw,
  };
}

export function apiKeyFunToResource(raw: SponsorProviderRaw): ProviderResource | null {
  return sponsorRawToResource('apikeyFun', raw, {
    displayName: APIKEY_FUN_DISPLAY_NAME,
    protocolLabels: APIKEY_FUN_PROTOCOLS,
    resolveBaseUrl: resolveApiKeyFunBaseUrl,
    getProtocolUrls: getApiKeyFunProtocolUrls,
  });
}

export function fennoAIToResource(raw: SponsorProviderRaw): ProviderResource | null {
  return sponsorRawToResource('fennoAI', raw, {
    displayName: FENNO_AI_DISPLAY_NAME,
    protocolLabels: FENNO_AI_PROTOCOL_LABELS,
    resolveBaseUrl: resolveFennoAIBaseUrl,
    getProtocolUrls: getFennoAIProtocolUrls,
  });
}

export function qiniuCloudToResource(raw: SponsorProviderRaw): ProviderResource | null {
  return sponsorRawToResource('qiniuCloud', raw, {
    displayName: QINIU_CLOUD_DISPLAY_NAME,
    protocolLabels: QINIU_CLOUD_PROTOCOL_LABELS,
    resolveBaseUrl: resolveQiniuCloudBaseUrl,
    getProtocolUrls: getQiniuCloudProtocolUrls,
  });
}

export function kimiToResource(raw: SponsorProviderRaw): ProviderResource | null {
  return sponsorRawToResource('kimi', raw, {
    displayName: KIMI_DISPLAY_NAME,
    protocolLabels: KIMI_PROTOCOL_LABELS,
    resolveBaseUrl: resolveKimiBaseUrl,
    getProtocolUrls: getKimiProtocolUrls,
  });
}
