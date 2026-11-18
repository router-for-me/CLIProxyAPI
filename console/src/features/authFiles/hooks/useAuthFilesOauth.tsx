import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { authFilesApi } from '@/services/api';
import { useNotificationStore } from '@/stores';
import type { OAuthModelAliasEntry } from '@/types';
import type { OAuthConfigLoadError } from '@/features/authFiles/constants';

export type UseAuthFilesOauthResult = {
  excluded: Record<string, string[]>;
  excludedError: OAuthConfigLoadError;
  modelAlias: Record<string, OAuthModelAliasEntry[]>;
  modelAliasError: OAuthConfigLoadError;
  loadExcluded: () => Promise<void>;
  loadModelAlias: () => Promise<void>;
};

/**
 * 认证文件页只**读取** OAuth 模型规则：给入口卡片提供摘要、给模型弹窗提供禁用状态。
 * 规则的编辑（草稿、校验、保存）全部在模型规则工作台里，见 features/authFiles/modelRules。
 */
export function useAuthFilesOauth(): UseAuthFilesOauthResult {
  const { t } = useTranslation();
  const showNotification = useNotificationStore((state) => state.showNotification);

  const [excluded, setExcluded] = useState<Record<string, string[]>>({});
  const [excludedError, setExcludedError] = useState<OAuthConfigLoadError>('loading');
  const [modelAlias, setModelAlias] = useState<Record<string, OAuthModelAliasEntry[]>>({});
  const [modelAliasError, setModelAliasError] = useState<OAuthConfigLoadError>('loading');

  const excludedUnsupportedRef = useRef(false);
  const mappingsUnsupportedRef = useRef(false);
  const excludedLoadRequestRef = useRef(0);
  const modelAliasLoadRequestRef = useRef(0);

  useEffect(
    () => () => {
      excludedLoadRequestRef.current += 1;
      modelAliasLoadRequestRef.current += 1;
    },
    []
  );

  const loadExcluded = useCallback(async () => {
    const requestId = ++excludedLoadRequestRef.current;
    setExcludedError('loading');
    try {
      const res = await authFilesApi.getOauthExcludedModels();
      if (requestId !== excludedLoadRequestRef.current) return;
      excludedUnsupportedRef.current = false;
      setExcluded(res || {});
      setExcludedError(null);
    } catch (err: unknown) {
      if (requestId !== excludedLoadRequestRef.current) return;
      const status =
        typeof err === 'object' && err !== null && 'status' in err
          ? (err as { status?: unknown }).status
          : undefined;

      if (status === 404) {
        setExcluded({});
        setExcludedError('unsupported');
        if (!excludedUnsupportedRef.current) {
          excludedUnsupportedRef.current = true;
          showNotification(t('oauth_excluded.upgrade_required'), 'warning');
        }
        return;
      }
      setExcludedError('load');
    }
  }, [showNotification, t]);

  const loadModelAlias = useCallback(async () => {
    const requestId = ++modelAliasLoadRequestRef.current;
    setModelAliasError('loading');
    try {
      const res = await authFilesApi.getOauthModelAlias();
      if (requestId !== modelAliasLoadRequestRef.current) return;
      mappingsUnsupportedRef.current = false;
      setModelAlias(res || {});
      setModelAliasError(null);
    } catch (err: unknown) {
      if (requestId !== modelAliasLoadRequestRef.current) return;
      const status =
        typeof err === 'object' && err !== null && 'status' in err
          ? (err as { status?: unknown }).status
          : undefined;

      if (status === 404) {
        setModelAlias({});
        setModelAliasError('unsupported');
        if (!mappingsUnsupportedRef.current) {
          mappingsUnsupportedRef.current = true;
          showNotification(t('oauth_model_alias.upgrade_required'), 'warning');
        }
        return;
      }
      setModelAliasError('load');
    }
  }, [showNotification, t]);

  return { excluded, excludedError, modelAlias, modelAliasError, loadExcluded, loadModelAlias };
}
