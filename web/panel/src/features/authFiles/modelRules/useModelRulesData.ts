import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { apiClient, authFilesApi } from '@/services/api';
import type { AuthFileItem, OAuthModelAliasEntry } from '@/types';
import { getErrorMessage } from '@/utils/helpers';
import type { AuthFileModelItem } from '../constants';
import { normalizeProviderKey } from '../constants';

export type ModelRulesKind = 'excluded' | 'alias';
export type ModelRulesStatus = 'loading' | 'ready' | 'unsupported' | 'error';

const getStatusCode = (err: unknown): unknown =>
  typeof err === 'object' && err !== null && 'status' in err
    ? (err as { status?: unknown }).status
    : undefined;

export type ModelRulesData = {
  status: ModelRulesStatus;
  errorMessage: string;
  files: AuthFileItem[];
  excluded: Record<string, string[]>;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  /** 每次成功载入 +1；页面据此把草稿重置到新基线。 */
  revision: number;
  /** 返回 false 表示这次载入已被更新的请求或连接切换作废。 */
  reload: () => Promise<boolean>;
};

/**
 * 工作台的基线数据。`kind` 对应的那份配置是「主」数据：它失败则整页不可编辑；
 * 另外两份只用来补全 provider 列表，失败时静默降级。
 */
export function useModelRulesData(kind: ModelRulesKind): ModelRulesData {
  const [status, setStatus] = useState<ModelRulesStatus>('loading');
  const [errorMessage, setErrorMessage] = useState('');
  const [files, setFiles] = useState<AuthFileItem[]>([]);
  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const [modelAlias, setModelAlias] = useState<Record<string, OAuthModelAliasEntry[]>>({});
  const [revision, setRevision] = useState(0);
  const requestRef = useRef(0);

  const reload = useCallback(async () => {
    const requestId = ++requestRef.current;
    const connection = apiClient.getConnectionRevision();
    const isCurrent = () =>
      requestId === requestRef.current && connection === apiClient.getConnectionRevision();

    const [filesResult, excludedResult, aliasResult] = await Promise.allSettled([
      authFilesApi.list(),
      authFilesApi.getOauthExcludedModels(),
      authFilesApi.getOauthModelAlias(),
    ]);
    if (!isCurrent()) return false;

    if (filesResult.status === 'fulfilled') setFiles(filesResult.value?.files ?? []);
    if (excludedResult.status === 'fulfilled') setExcluded(excludedResult.value ?? {});
    if (aliasResult.status === 'fulfilled') setModelAlias(aliasResult.value ?? {});

    const primary = kind === 'excluded' ? excludedResult : aliasResult;
    if (primary.status === 'fulfilled') {
      setErrorMessage('');
      setStatus('ready');
      setRevision((value) => value + 1);
      return true;
    }
    if (getStatusCode(primary.reason) === 404) {
      setStatus('unsupported');
      return true;
    }
    setErrorMessage(getErrorMessage(primary.reason));
    setStatus('error');
    return true;
  }, [kind]);

  useEffect(() => {
    void reload();
    return () => {
      requestRef.current += 1;
    };
  }, [reload]);

  return { status, errorMessage, files, excluded, modelAlias, revision, reload };
}

export type ProviderCatalogState = 'loading' | 'ready' | 'unavailable' | 'error';
export type ProviderCatalog = Record<
  string,
  { state: ProviderCatalogState; models: AuthFileModelItem[] }
>;

/**
 * 按需加载各 provider 的模型目录（自动补全 / 图谱里的未映射源模型）。
 * 每个 provider 只请求一次（失败后可 retry）；连接切换后旧响应不会写回。
 * 400/404 = 该 provider 没有目录（仍可手动输入）；其余失败 = 可重试的错误。
 */
export function useProviderModelCatalog(providers: readonly string[], enabled = true) {
  const [catalog, setCatalog] = useState<ProviderCatalog>({});
  const [retryTick, setRetryTick] = useState(0);
  const requestedRef = useRef(new Set<string>());
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const providerKeys = useMemo(
    () =>
      Array.from(new Set(providers.map((provider) => normalizeProviderKey(provider))))
        .filter(Boolean)
        .sort()
        .join('\n'),
    [providers]
  );

  useEffect(() => {
    if (!enabled || !providerKeys) return;
    const connection = apiClient.getConnectionRevision();
    const pending = providerKeys.split('\n').filter((key) => !requestedRef.current.has(key));
    if (pending.length === 0) return;

    pending.forEach((key) => requestedRef.current.add(key));
    setCatalog((current) => ({
      ...current,
      ...Object.fromEntries(pending.map((key) => [key, { state: 'loading', models: [] }])),
    }));

    pending.forEach((key) => {
      authFilesApi
        .getModelDefinitions(key)
        .then((models) => ({ state: 'ready' as ProviderCatalogState, models }))
        .catch((err: unknown) => {
          const code = getStatusCode(err);
          const state: ProviderCatalogState =
            code === 400 || code === 404 ? 'unavailable' : 'error';
          return { state, models: [] as AuthFileModelItem[] };
        })
        .then((entry) => {
          if (!mountedRef.current || connection !== apiClient.getConnectionRevision()) return;
          setCatalog((current) => ({ ...current, [key]: entry }));
        });
    });
  }, [enabled, providerKeys, retryTick]);

  const retry = useCallback((provider: string) => {
    requestedRef.current.delete(normalizeProviderKey(provider));
    setRetryTick((value) => value + 1);
  }, []);

  return { catalog, retry };
}

export type SaveOutcome = { failed: string[]; message: string };

/** 逐个 provider 提交；单个失败不影响其余项，连接变化则返回 null 并终止整个批次。 */
export async function runSavePlan<T>(
  plan: readonly { provider: string; value: T | null }[],
  save: (provider: string, value: T) => Promise<unknown>,
  remove: (provider: string) => Promise<unknown>
): Promise<SaveOutcome | null> {
  const connection = apiClient.getConnectionRevision();
  const isCurrent = () => connection === apiClient.getConnectionRevision();
  const failed: string[] = [];
  let message = '';
  for (const { provider, value } of plan) {
    if (!isCurrent()) return null;
    try {
      if (value === null) await remove(provider);
      else await save(provider, value);
    } catch (err: unknown) {
      failed.push(provider);
      if (!message) message = getErrorMessage(err);
    }
  }
  return isCurrent() ? { failed, message } : null;
}
