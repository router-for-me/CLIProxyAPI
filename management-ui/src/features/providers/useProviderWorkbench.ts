import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { providersApi } from '@/services/api';
import { getErrorMessage } from '@/utils/helpers';
import { useAuthStore, useConfigStore } from '@/stores';
import {
  stripDisableAllModelsRule,
  withDisableAllModelsRule,
  withoutDisableAllModelsRule,
} from '@/components/providers/utils';
import type {
  Config,
  GeminiKeyConfig,
  ModelAlias,
  OpenAIProviderConfig,
  ProviderKeyConfig,
} from '@/types';
import {
  apiKeyFunToResource,
  claudeToResource,
  codexToResource,
  fennoAIToResource,
  geminiToResource,
  interactionsToResource,
  metaToResource,
  openaiToResource,
  qiniuCloudToResource,
  kimiToResource,
  vertexToResource,
  xaiToResource,
} from './adapters';
import { PROVIDER_BRAND_ORDER, PROVIDER_DESCRIPTORS } from './descriptors';
import { buildRuntimePolicy } from './runtimePolicy';
import { buildModelOptions } from './modelOptions';
import { pickProviderBehavior } from './providerBehavior';
import type {
  ProviderBrand,
  ProviderEntryFormInput,
  ProviderGroup,
  ProviderResource,
  ProviderSnapshot,
  SponsorKeyEntryInput,
  SponsorProviderBrand,
  SponsorProviderRaw,
} from './types';
import {
  buildApiKeyFunRaw,
  isApiKeyFunClaudeProvider,
  isApiKeyFunCodexProvider,
  isApiKeyFunOpenAIProvider,
} from './sponsor';
import { buildFennoAIRaw, isFennoAIClaudeProvider, isFennoAICodexProvider } from './fennoAI';
import {
  buildQiniuCloudRaw,
  isQiniuCloudClaudeProvider,
  isQiniuCloudCodexProvider,
  isQiniuCloudGeminiProvider,
  isQiniuCloudOpenAIProvider,
} from './qiniuCloud';
import {
  buildKimiRaw,
  isKimiClaudeProvider,
  isKimiCodexProvider,
  isKimiOpenAIProvider,
} from './kimi';
import { getSponsorProviderDefinition, type SponsorProtocolUrls } from './sponsorDefinitions';
import { runSponsorMutationWithRecovery } from './sponsorMutationRecovery';

export interface UseProviderWorkbenchResult {
  connected: boolean;
  isPending: boolean;
  isFetching: boolean;
  isError: boolean;
  errorMessage: string | null;
  snapshot: ProviderSnapshot | null;
  refetch: () => Promise<void>;

  createProvider: (brand: ProviderBrand, input: ProviderEntryFormInput) => Promise<void>;
  updateProvider: (resource: ProviderResource, input: ProviderEntryFormInput) => Promise<void>;
  deleteProvider: (resource: ProviderResource) => Promise<void>;
  toggleDisabled: (resource: ProviderResource, disabled: boolean) => Promise<void>;
  mutating: boolean;
  refreshSnapshot: () => void;
}

/* -------------------------------------------------------------------------- */
/* form -> backend config 转换                                                 */
/* -------------------------------------------------------------------------- */

const parseTextList = (text: string): string[] =>
  text
    .split(/[\n,]+/)
    .map((item) => item.trim())
    .filter(Boolean);

const headersFromEntries = (
  entries: Array<{ key: string; value: string }>
): Record<string, string> => {
  const out: Record<string, string> = {};
  entries.forEach((entry) => {
    const key = entry.key.trim();
    if (!key) return;
    out[key] = entry.value;
  });
  return out;
};

/**
 * `'*'` 是「该 provider 已停用」的编码，其唯一所有者是 `form.disabled`：
 * 载入时 `stripDisableAllModelsRule` 把它剥进该 flag，保存时仅凭该 flag 重新追加。
 * 因此这里必须过滤掉用户在文本里手打的 `'*'`——排除模型的编辑面永远不该能开关停用。
 * 导出仅为让 tests/providerExcludedModelsDisableRule.test.ts 钉住这个不变量。
 */
export const buildExcludedModels = (
  textValue: string,
  disabled: boolean,
  brand: ProviderBrand
): string[] | undefined => {
  const list = parseTextList(textValue);
  const filtered = list.filter((v) => v !== '*');
  if (brand === 'openaiCompatibility') {
    return filtered.length ? filtered : undefined;
  }
  if (disabled) {
    return withDisableAllModelsRule(filtered);
  }
  return filtered.length ? filtered : undefined;
};

const buildModelAliases = (
  models: ProviderEntryFormInput['models'] | undefined,
  includeImage = false
): ModelAlias[] =>
  (models ?? [])
    .map((m) => {
      const entry: ModelAlias = {
        sourceIndex: m.sourceIndex ?? null,
        name: m.name.trim(),
        alias: m.alias?.trim() || undefined,
        priority: m.priority,
        ...buildModelOptions(m),
      };
      if (includeImage) {
        entry.image = m.image === true;
      }
      return entry;
    })
    .filter((m) => m.name);

const buildProviderKeyConfig = (
  brand: 'gemini' | 'interactions' | 'codex' | 'meta' | 'xai' | 'claude' | 'vertex',
  input: ProviderEntryFormInput,
  existing?: ProviderKeyConfig | GeminiKeyConfig | null
): ProviderKeyConfig | GeminiKeyConfig => {
  const headers = headersFromEntries(input.headers);
  const models = buildModelAliases(input.models);
  const excluded = buildExcludedModels(input.excludedModelsText, input.disabled, brand);
  const apiKeyChanged = input.apiKey.trim().length > 0;
  const next: ProviderKeyConfig = {
    source: existing?.source,
    apiKey: apiKeyChanged ? input.apiKey.trim() : (existing?.apiKey ?? ''),
    priority: input.priority,
    weight: input.weight,
    prefix: input.prefix.trim() || undefined,
    baseUrl: input.baseUrl.trim() || undefined,
    proxyUrl: input.proxyUrl.trim() || undefined,
    models: models.length ? models : undefined,
    headers: Object.keys(headers).length ? headers : undefined,
    excludedModels: excluded,
    disableCooling: input.disableCooling,
    ...(input.runtimePolicy
      ? buildRuntimePolicy(
          input.runtimePolicy,
          PROVIDER_DESCRIPTORS[brand].supportsRequestScopedErrors
        )
      : {}),
    authIndex: existing?.authIndex,
    ...pickProviderBehavior(input, brand),
  };
  if ((brand === 'codex' || brand === 'xai') && input.websockets !== undefined) {
    next.websockets = input.websockets;
  }
  if (brand === 'claude' && input.cloak) {
    next.cloak = {
      mode: input.cloak.mode.trim() || undefined,
      strictMode: input.cloak.strictMode,
      sensitiveWords: parseTextList(input.cloak.sensitiveWordsText),
      cacheUserId: input.cloak.cacheUserId === true,
    };
  }
  if (brand === 'claude') {
    next.fingerprintProfile = input.fingerprintProfile?.trim() || undefined;
  }
  return next;
};

const buildOpenAIConfig = (
  input: ProviderEntryFormInput,
  existing?: OpenAIProviderConfig | null
): OpenAIProviderConfig => {
  const headers = headersFromEntries(input.headers);
  const models = buildModelAliases(input.models, true);
  const apiKeyEntries =
    input.apiKeyEntries
      ?.map((entry, index) => {
        const fallbackApiKey =
          entry.existingApiKey?.trim() || existing?.apiKeyEntries?.[index]?.apiKey?.trim() || '';
        return {
          apiKey: entry.apiKey.trim() || fallbackApiKey,
          sourceIndex: entry.sourceIndex,
          proxyUrl: entry.proxyUrl.trim() || undefined,
          weight: entry.weight,
          authIndex: entry.authIndex?.trim() || undefined,
        };
      })
      .filter((entry) => entry.apiKey) ?? [];

  return {
    ...(existing ?? {}),
    name: input.name.trim(),
    baseUrl: input.baseUrl.trim(),
    prefix: input.prefix.trim() || undefined,
    apiKeyEntries,
    disabled: input.disabled,
    ...pickProviderBehavior(input, 'openaiCompatibility'),
    disableCooling: input.disableCooling,
    ...(input.runtimePolicy ? buildRuntimePolicy(input.runtimePolicy) : {}),
    headers: Object.keys(headers).length ? headers : undefined,
    models: models.length ? models : undefined,
    priority: input.priority,
  };
};

const sponsorEntryApiKey = (entry: SponsorKeyEntryInput): string =>
  entry.apiKey.trim() || entry.existingApiKey?.trim() || '';

const buildSponsorOpenAIConfig = (
  entry: SponsorKeyEntryInput,
  providerName: string,
  getProtocolUrls: (value: string | undefined | null) => SponsorProtocolUrls,
  existing?: OpenAIProviderConfig
): OpenAIProviderConfig => {
  const urls = getProtocolUrls(entry.baseUrl);
  const models = buildModelAliases(entry.models, true);
  const apiKey = sponsorEntryApiKey(entry);
  const firstExistingEntry = existing?.apiKeyEntries?.[0];
  const apiKeyEntries = apiKey
    ? [
        {
          ...(firstExistingEntry ?? {}),
          apiKey,
          proxyUrl: entry.proxyUrl.trim() || undefined,
          weight: entry.weight,
        },
      ]
    : [];

  return {
    ...(existing ?? {}),
    name: providerName,
    baseUrl: urls.openai,
    ...pickProviderBehavior(entry, 'openaiCompatibility'),
    prefix: entry.prefix.trim() || undefined,
    disabled: entry.disabled,
    disableCooling: entry.disableCooling,
    ...(entry.runtimePolicy ? buildRuntimePolicy(entry.runtimePolicy) : {}),
    priority: entry.priority,
    apiKeyEntries,
    models: models.length ? models : undefined,
  };
};

const buildSponsorProviderKeyConfig = (
  entry: SponsorKeyEntryInput,
  protocol: 'claude' | 'codex',
  getProtocolUrls: (value: string | undefined | null) => SponsorProtocolUrls,
  existing?: ProviderKeyConfig
): ProviderKeyConfig => {
  const urls = getProtocolUrls(entry.baseUrl);
  const models = buildModelAliases(entry.models);
  const apiKey = sponsorEntryApiKey(entry);
  const excluded = entry.disabled
    ? withDisableAllModelsRule(stripDisableAllModelsRule(existing?.excludedModels))
    : withoutDisableAllModelsRule(existing?.excludedModels);

  return {
    ...(existing ?? {}),
    apiKey,
    baseUrl: protocol === 'claude' ? urls.anthropic : urls.codex,
    ...pickProviderBehavior(entry, protocol),
    proxyUrl: entry.proxyUrl.trim() || undefined,
    prefix: entry.prefix.trim() || undefined,
    priority: entry.priority,
    weight: entry.weight,
    disableCooling: entry.disableCooling,
    ...(entry.runtimePolicy ? buildRuntimePolicy(entry.runtimePolicy) : {}),
    excludedModels: excluded,
    models: models.length ? models : undefined,
  };
};

const buildSponsorGeminiConfig = (
  entry: SponsorKeyEntryInput,
  getProtocolUrls: (value: string | undefined | null) => SponsorProtocolUrls,
  existing?: GeminiKeyConfig
): GeminiKeyConfig => {
  const urls = getProtocolUrls(entry.baseUrl);
  const models = buildModelAliases(entry.models);
  const apiKey = sponsorEntryApiKey(entry);
  const excluded = entry.disabled
    ? withDisableAllModelsRule(stripDisableAllModelsRule(existing?.excludedModels))
    : withoutDisableAllModelsRule(existing?.excludedModels);

  return {
    ...(existing ?? {}),
    apiKey,
    baseUrl: urls.gemini,
    proxyUrl: entry.proxyUrl.trim() || undefined,
    prefix: entry.prefix.trim() || undefined,
    priority: entry.priority,
    weight: entry.weight,
    disableCooling: entry.disableCooling,
    ...(entry.runtimePolicy ? buildRuntimePolicy(entry.runtimePolicy) : {}),
    excludedModels: excluded,
    models: models.length ? models : undefined,
  };
};

const normalizeSponsorKeyEntries = (
  entries: SponsorKeyEntryInput[] | undefined
): SponsorKeyEntryInput[] => (entries ?? []).filter((entry) => sponsorEntryApiKey(entry));

const toggleSponsorConfig = async (raw: SponsorProviderRaw, disabled: boolean) => {
  for (const item of raw.gemini) {
    const excludedModels = disabled
      ? withDisableAllModelsRule(item.config.excludedModels)
      : withoutDisableAllModelsRule(item.config.excludedModels);
    await providersApi.updateGeminiKey(item.config.apiKey, item.config.baseUrl, {
      ...item.config,
      excludedModels,
    });
  }
  for (const item of raw.codex) {
    const excludedModels = disabled
      ? withDisableAllModelsRule(item.config.excludedModels)
      : withoutDisableAllModelsRule(item.config.excludedModels);
    await providersApi.updateCodexConfig(item.config.apiKey, item.config.baseUrl, {
      ...item.config,
      excludedModels,
    });
  }
  for (const item of raw.claude) {
    const excludedModels = disabled
      ? withDisableAllModelsRule(item.config.excludedModels)
      : withoutDisableAllModelsRule(item.config.excludedModels);
    await providersApi.updateClaudeConfig(item.config.apiKey, item.config.baseUrl, {
      ...item.config,
      excludedModels,
    });
  }
  for (const item of raw.openai) {
    await providersApi.updateOpenAIProviderDisabled(item.index, disabled, item.config.source);
  }
};

export const buildProviderGroups = (config: Config): ProviderGroup[] =>
  PROVIDER_BRAND_ORDER.reduce<ProviderGroup[]>((groups, brand) => {
    let resources: ProviderResource[];
    switch (brand) {
      case 'gemini':
        resources = (config.geminiApiKeys ?? []).reduce<ProviderResource[]>((out, item, index) => {
          if (!isQiniuCloudGeminiProvider(item)) {
            out.push(geminiToResource(item, index));
          }
          return out;
        }, []);
        break;
      case 'interactions':
        resources = (config.interactionsApiKeys ?? []).map((item, index) =>
          interactionsToResource(item, index)
        );
        break;
      case 'codex':
        resources = (config.codexApiKeys ?? []).reduce<ProviderResource[]>((out, item, index) => {
          if (
            !isApiKeyFunCodexProvider(item) &&
            !isFennoAICodexProvider(item) &&
            !isQiniuCloudCodexProvider(item) &&
            !isKimiCodexProvider(item)
          ) {
            out.push(codexToResource(item, index));
          }
          return out;
        }, []);
        break;
      case 'meta':
        resources = (config.metaApiKeys ?? []).map((item, index) => metaToResource(item, index));
        break;
      case 'xai':
        resources = (config.xaiApiKeys ?? []).map((item, index) => xaiToResource(item, index));
        break;
      case 'claude':
        resources = (config.claudeApiKeys ?? []).reduce<ProviderResource[]>((out, item, index) => {
          if (
            !isApiKeyFunClaudeProvider(item) &&
            !isFennoAIClaudeProvider(item) &&
            !isQiniuCloudClaudeProvider(item) &&
            !isKimiClaudeProvider(item)
          ) {
            out.push(claudeToResource(item, index));
          }
          return out;
        }, []);
        break;
      case 'vertex':
        resources = (config.vertexApiKeys ?? []).map((item, index) =>
          vertexToResource(item, index)
        );
        break;
      case 'openaiCompatibility':
        resources = (config.openaiCompatibility ?? []).reduce<ProviderResource[]>(
          (out, item, index) => {
            if (
              !isApiKeyFunOpenAIProvider(item) &&
              !isQiniuCloudOpenAIProvider(item) &&
              !isKimiOpenAIProvider(item)
            ) {
              out.push(openaiToResource(item, index));
            }
            return out;
          },
          []
        );
        break;
      case 'apikeyFun': {
        const sponsorResource = apiKeyFunToResource(buildApiKeyFunRaw(config));
        resources = sponsorResource ? [sponsorResource] : [];
        break;
      }
      case 'fennoAI': {
        const sponsorResource = fennoAIToResource(buildFennoAIRaw(config));
        resources = sponsorResource ? [sponsorResource] : [];
        break;
      }
      case 'qiniuCloud': {
        const sponsorResource = qiniuCloudToResource(buildQiniuCloudRaw(config));
        resources = sponsorResource ? [sponsorResource] : [];
        break;
      }
      case 'kimi': {
        const sponsorResource = kimiToResource(buildKimiRaw(config));
        resources = sponsorResource ? [sponsorResource] : [];
        break;
      }
      default:
        return groups;
    }
    groups.push({
      id: brand,
      resources,
    });
    return groups;
  }, []);

/* -------------------------------------------------------------------------- */
/* hook                                                                       */
/* -------------------------------------------------------------------------- */

export function useProviderWorkbench(): UseProviderWorkbenchResult {
  const connectionStatus = useAuthStore((s) => s.connectionStatus);
  const config = useConfigStore((s) => s.config);
  const fetchConfig = useConfigStore((s) => s.fetchConfig);
  const updateConfigValue = useConfigStore((s) => s.updateConfigValue);
  const isCacheValid = useConfigStore((s) => s.isCacheValid);

  const [isPending, setIsPending] = useState<boolean>(() => !isCacheValid());
  const [isFetching, setIsFetching] = useState<boolean>(false);
  const [errorMessage, setErrorMessage] = useState<string | null>(null);
  const [mutating, setMutating] = useState<boolean>(false);
  const [fetchedAt, setFetchedAt] = useState<string>(() => new Date().toISOString());

  const hasFetchedRef = useRef(false);

  const connected = connectionStatus === 'connected';

  const refetch = useCallback(async () => {
    setIsFetching(true);
    setErrorMessage(null);
    try {
      const [configResult, vertexResult, openaiResult] = await Promise.allSettled([
        fetchConfig(true),
        providersApi.getVertexConfigs(),
        providersApi.getOpenAIProviders(),
      ]);
      if (configResult.status !== 'fulfilled') {
        throw configResult.reason;
      }
      if (vertexResult.status === 'fulfilled') {
        updateConfigValue('vertex-api-key', vertexResult.value || []);
      }
      if (openaiResult.status === 'fulfilled') {
        updateConfigValue('openai-compatibility', openaiResult.value || []);
      }
      setFetchedAt(new Date().toISOString());
    } catch (err) {
      setErrorMessage(getErrorMessage(err) || 'Failed to load providers');
    } finally {
      setIsPending(false);
      setIsFetching(false);
    }
  }, [fetchConfig, updateConfigValue]);

  const refreshSnapshot = useCallback(() => {
    setFetchedAt(new Date().toISOString());
  }, []);

  useEffect(() => {
    if (hasFetchedRef.current) return;
    if (!connected) return;
    hasFetchedRef.current = true;
    refetch().catch(() => {});
  }, [connected, refetch]);

  /* ------------------- snapshot 计算 ------------------- */

  const snapshot = useMemo<ProviderSnapshot | null>(() => {
    if (!config) return null;
    return {
      fetchedAt,
      groups: buildProviderGroups(config),
    };
  }, [config, fetchedAt]);

  /* ------------------- mutations ------------------- */

  const persistSponsorConfig = useCallback(
    async (brand: SponsorProviderBrand, input: ProviderEntryFormInput) => {
      const definition = getSponsorProviderDefinition(brand);
      const raw =
        brand === 'apikeyFun'
          ? buildApiKeyFunRaw(config)
          : brand === 'fennoAI'
            ? buildFennoAIRaw(config)
            : brand === 'qiniuCloud'
              ? buildQiniuCloudRaw(config)
              : buildKimiRaw(config);
      const entries = normalizeSponsorKeyEntries(input.sponsorKeyEntries);
      const openaiEntry = entries.find((entry) => entry.protocol === 'openai');
      const claudeEntry = entries.find((entry) => entry.protocol === 'claude');
      const codexEntry = entries.find((entry) => entry.protocol === 'codex');
      const geminiEntry = entries.find((entry) => entry.protocol === 'gemini');

      if (definition.protocols.includes('gemini')) {
        const current = raw.gemini[0];
        if (geminiEntry) {
          const next = buildSponsorGeminiConfig(
            geminiEntry,
            definition.getProtocolUrls,
            current?.config
          );
          if (current) {
            await providersApi.updateGeminiKey(current.config.apiKey, current.config.baseUrl, {
              ...next,
              source: current.config.source,
            });
          } else {
            await providersApi.createGeminiKey(next);
          }
        } else {
          for (const item of raw.gemini) {
            await providersApi.deleteGeminiKey(
              item.config.apiKey,
              item.config.baseUrl,
              item.config.source
            );
          }
        }
      }

      const currentCodex = raw.codex[0];
      if (codexEntry) {
        const next = buildSponsorProviderKeyConfig(
          codexEntry,
          'codex',
          definition.getProtocolUrls,
          currentCodex?.config
        );
        if (currentCodex) {
          await providersApi.updateCodexConfig(
            currentCodex.config.apiKey,
            currentCodex.config.baseUrl,
            { ...next, source: currentCodex.config.source }
          );
        } else {
          await providersApi.createCodexConfig(next);
        }
      } else {
        for (const item of raw.codex) {
          await providersApi.deleteCodexConfig(
            item.config.apiKey,
            item.config.baseUrl,
            item.config.source
          );
        }
      }

      const currentClaude = raw.claude[0];
      if (claudeEntry) {
        const next = buildSponsorProviderKeyConfig(
          claudeEntry,
          'claude',
          definition.getProtocolUrls,
          currentClaude?.config
        );
        if (currentClaude) {
          await providersApi.updateClaudeConfig(
            currentClaude.config.apiKey,
            currentClaude.config.baseUrl,
            { ...next, source: currentClaude.config.source }
          );
        } else {
          await providersApi.createClaudeConfig(next);
        }
      } else {
        for (const item of raw.claude) {
          await providersApi.deleteClaudeConfig(
            item.config.apiKey,
            item.config.baseUrl,
            item.config.source
          );
        }
      }

      const currentOpenAI = raw.openai[0];
      if (openaiEntry) {
        const next = buildSponsorOpenAIConfig(
          openaiEntry,
          definition.providerName,
          definition.getProtocolUrls,
          currentOpenAI?.config
        );
        if (currentOpenAI) {
          await providersApi.updateOpenAIProvider(
            currentOpenAI.config.name,
            currentOpenAI.index,
            next
          );
        } else {
          await providersApi.createOpenAIProvider(next);
        }
      } else if (currentOpenAI) {
        await providersApi.deleteOpenAIProvider(currentOpenAI.index, currentOpenAI.config.source);
      }
    },
    [config]
  );

  const createProvider = useCallback(
    async (brand: ProviderBrand, input: ProviderEntryFormInput) => {
      setMutating(true);
      try {
        if (brand === 'gemini') {
          await providersApi.createGeminiKey(
            buildProviderKeyConfig('gemini', input) as GeminiKeyConfig
          );
        } else if (brand === 'interactions') {
          await providersApi.createInteractionsKey(
            buildProviderKeyConfig('interactions', input) as GeminiKeyConfig
          );
        } else if (brand === 'codex') {
          await providersApi.createCodexConfig(
            buildProviderKeyConfig('codex', input) as ProviderKeyConfig
          );
        } else if (brand === 'meta') {
          await providersApi.createMetaConfig(
            buildProviderKeyConfig('meta', input) as ProviderKeyConfig
          );
        } else if (brand === 'xai') {
          await providersApi.createXAIConfig(
            buildProviderKeyConfig('xai', input) as ProviderKeyConfig
          );
        } else if (brand === 'claude') {
          await providersApi.createClaudeConfig(
            buildProviderKeyConfig('claude', input) as ProviderKeyConfig
          );
        } else if (brand === 'vertex') {
          await providersApi.createVertexConfig(
            buildProviderKeyConfig('vertex', input) as ProviderKeyConfig
          );
        } else if (brand === 'openaiCompatibility') {
          await providersApi.createOpenAIProvider(buildOpenAIConfig(input));
        } else if (
          brand === 'apikeyFun' ||
          brand === 'fennoAI' ||
          brand === 'qiniuCloud' ||
          brand === 'kimi'
        ) {
          await runSponsorMutationWithRecovery(() => persistSponsorConfig(brand, input), refetch);
        }
        await refetch();
      } finally {
        setMutating(false);
      }
    },
    [persistSponsorConfig, refetch]
  );

  const updateProvider = useCallback(
    async (resource: ProviderResource, input: ProviderEntryFormInput) => {
      setMutating(true);
      try {
        const brand = resource.brand;
        const selector = resource.selector;
        if (brand === 'gemini' && selector.brand === 'gemini') {
          const existing = resource.raw as GeminiKeyConfig;
          await providersApi.updateGeminiKey(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('gemini', input, existing) as GeminiKeyConfig
          );
        } else if (brand === 'interactions' && selector.brand === 'interactions') {
          const existing = resource.raw as GeminiKeyConfig;
          await providersApi.updateInteractionsKey(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('interactions', input, existing) as GeminiKeyConfig
          );
        } else if (brand === 'codex' && selector.brand === 'codex') {
          const existing = resource.raw as ProviderKeyConfig;
          await providersApi.updateCodexConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('codex', input, existing) as ProviderKeyConfig
          );
        } else if (brand === 'meta' && selector.brand === 'meta') {
          const existing = resource.raw as ProviderKeyConfig;
          await providersApi.updateMetaConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('meta', input, existing) as ProviderKeyConfig
          );
        } else if (brand === 'xai' && selector.brand === 'xai') {
          const existing = resource.raw as ProviderKeyConfig;
          await providersApi.updateXAIConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('xai', input, existing) as ProviderKeyConfig
          );
        } else if (brand === 'claude' && selector.brand === 'claude') {
          const existing = resource.raw as ProviderKeyConfig;
          await providersApi.updateClaudeConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('claude', input, existing) as ProviderKeyConfig
          );
        } else if (brand === 'vertex' && selector.brand === 'vertex') {
          const existing = resource.raw as ProviderKeyConfig;
          await providersApi.updateVertexConfig(
            selector.apiKey,
            selector.baseUrl,
            buildProviderKeyConfig('vertex', input, existing) as ProviderKeyConfig
          );
        } else if (brand === 'openaiCompatibility' && selector.brand === 'openaiCompatibility') {
          await providersApi.updateOpenAIProvider(
            selector.name,
            selector.index,
            buildOpenAIConfig(input, resource.raw as OpenAIProviderConfig)
          );
        } else if (
          brand === 'apikeyFun' ||
          brand === 'fennoAI' ||
          brand === 'qiniuCloud' ||
          brand === 'kimi'
        ) {
          await runSponsorMutationWithRecovery(() => persistSponsorConfig(brand, input), refetch);
        }
        await refetch();
      } finally {
        setMutating(false);
      }
    },
    [persistSponsorConfig, refetch]
  );

  const deleteProvider = useCallback(
    async (resource: ProviderResource) => {
      setMutating(true);
      try {
        const sel = resource.selector;
        if (sel.brand === 'gemini') {
          await providersApi.deleteGeminiKey(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.geminiApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('gemini-api-key', next);
        } else if (sel.brand === 'interactions') {
          await providersApi.deleteInteractionsKey(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.interactionsApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('interactions-api-key', next);
        } else if (sel.brand === 'codex') {
          await providersApi.deleteCodexConfig(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.codexApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('codex-api-key', next);
        } else if (sel.brand === 'meta') {
          await providersApi.deleteMetaConfig(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.metaApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('meta-api-key', next);
        } else if (sel.brand === 'xai') {
          await providersApi.deleteXAIConfig(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.xaiApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('xai-api-key', next);
        } else if (sel.brand === 'claude') {
          await providersApi.deleteClaudeConfig(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.claudeApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('claude-api-key', next);
        } else if (sel.brand === 'vertex') {
          await providersApi.deleteVertexConfig(
            sel.apiKey,
            sel.baseUrl,
            (resource.raw as ProviderKeyConfig).source
          );
          const next = (config?.vertexApiKeys ?? []).filter((_, i) => i !== sel.index);
          updateConfigValue('vertex-api-key', next);
        } else if (sel.brand === 'openaiCompatibility') {
          await providersApi.deleteOpenAIProvider(
            sel.index,
            (resource.raw as OpenAIProviderConfig).source
          );
          const next = (config?.openaiCompatibility ?? []).filter(
            (item, index) => (item.sourceIndex ?? index) !== sel.index
          );
          updateConfigValue('openai-compatibility', next);
        } else if (
          sel.brand === 'apikeyFun' ||
          sel.brand === 'fennoAI' ||
          sel.brand === 'qiniuCloud' ||
          sel.brand === 'kimi'
        ) {
          await runSponsorMutationWithRecovery(async () => {
            const raw = resource.raw as SponsorProviderRaw;
            for (const item of raw.gemini) {
              await providersApi.deleteGeminiKey(
                item.config.apiKey,
                item.config.baseUrl,
                item.config.source
              );
            }
            for (const item of raw.codex) {
              await providersApi.deleteCodexConfig(
                item.config.apiKey,
                item.config.baseUrl,
                item.config.source
              );
            }
            for (const item of raw.claude) {
              await providersApi.deleteClaudeConfig(
                item.config.apiKey,
                item.config.baseUrl,
                item.config.source
              );
            }
            const openAIIndices = raw.openai
              .map((item) => item.index)
              .sort((left, right) => right - left);
            for (const index of openAIIndices) {
              await providersApi.deleteOpenAIProvider(
                index,
                raw.openai.find((item) => item.index === index)?.config.source
              );
            }
          }, refetch);
        }
        await refetch();
      } finally {
        setMutating(false);
      }
    },
    [config, refetch, updateConfigValue]
  );

  const toggleDisabled = useCallback(
    async (resource: ProviderResource, disabled: boolean) => {
      setMutating(true);
      try {
        const brand = resource.brand;
        const selector = resource.selector;
        if (brand === 'gemini' && selector.brand === 'gemini') {
          const current = resource.raw as GeminiKeyConfig;
          const excluded = disabled
            ? withDisableAllModelsRule(current.excludedModels)
            : withoutDisableAllModelsRule(current.excludedModels);
          await providersApi.updateGeminiKey(selector.apiKey, selector.baseUrl, {
            ...current,
            excludedModels: excluded,
          });
        } else if (brand === 'interactions' && selector.brand === 'interactions') {
          const current = resource.raw as GeminiKeyConfig;
          const excluded = disabled
            ? withDisableAllModelsRule(current.excludedModels)
            : withoutDisableAllModelsRule(current.excludedModels);
          await providersApi.updateInteractionsKey(selector.apiKey, selector.baseUrl, {
            ...current,
            excludedModels: excluded,
          });
        } else if (
          (brand === 'codex' && selector.brand === 'codex') ||
          (brand === 'meta' && selector.brand === 'meta') ||
          (brand === 'xai' && selector.brand === 'xai') ||
          (brand === 'claude' && selector.brand === 'claude') ||
          (brand === 'vertex' && selector.brand === 'vertex')
        ) {
          const current = resource.raw as ProviderKeyConfig;
          const excluded = disabled
            ? withDisableAllModelsRule(current.excludedModels)
            : withoutDisableAllModelsRule(current.excludedModels);
          const next = { ...current, excludedModels: excluded };
          if (selector.brand === 'codex') {
            await providersApi.updateCodexConfig(selector.apiKey, selector.baseUrl, next);
          } else if (selector.brand === 'meta') {
            await providersApi.updateMetaConfig(selector.apiKey, selector.baseUrl, next);
          } else if (selector.brand === 'xai') {
            await providersApi.updateXAIConfig(selector.apiKey, selector.baseUrl, next);
          } else if (selector.brand === 'claude') {
            await providersApi.updateClaudeConfig(selector.apiKey, selector.baseUrl, next);
          } else if (selector.brand === 'vertex') {
            await providersApi.updateVertexConfig(selector.apiKey, selector.baseUrl, next);
          }
        } else if (brand === 'openaiCompatibility' && selector.brand === 'openaiCompatibility') {
          await providersApi.updateOpenAIProviderDisabled(
            selector.index,
            disabled,
            (resource.raw as OpenAIProviderConfig).source
          );
        } else if (
          brand === 'apikeyFun' ||
          brand === 'fennoAI' ||
          brand === 'qiniuCloud' ||
          brand === 'kimi'
        ) {
          await runSponsorMutationWithRecovery(
            () => toggleSponsorConfig(resource.raw as SponsorProviderRaw, disabled),
            refetch
          );
        }
        await refetch();
      } finally {
        setMutating(false);
      }
    },
    [refetch]
  );

  return {
    connected,
    isPending,
    isFetching,
    isError: Boolean(errorMessage),
    errorMessage,
    snapshot,
    refetch,
    createProvider,
    updateProvider,
    deleteProvider,
    toggleDisabled,
    mutating,
    refreshSnapshot,
  };
}
