import { useCallback, useMemo, useState } from 'react';
import { useLocation, useNavigate, useSearchParams } from 'react-router-dom';
import type { TFunction } from 'i18next';
import { useTranslation } from 'react-i18next';
import { useUnsavedChangesGuard } from '@/hooks/useUnsavedChangesGuard';
import type { AuthFileItem, ResolvedTheme } from '@/types';
import {
  buildOAuthProviderOptions,
  getAuthFileIcon,
  getTypeLabel,
  normalizeProviderKey,
} from '../constants';
import type { ProviderRailItem } from './ModelRulesWorkspace';
import type { ModelRulesKind } from './useModelRulesData';

export const MODEL_RULES_PATHS: Record<ModelRulesKind, string> = {
  excluded: '/auth-files/oauth-excluded',
  alias: '/auth-files/oauth-model-alias',
};

export const buildModelRulesPath = (kind: ModelRulesKind, provider?: string): string => {
  const providerKey = normalizeProviderKey(provider ?? '');
  const search = providerKey ? `?provider=${encodeURIComponent(providerKey)}` : '';
  return `${MODEL_RULES_PATHS[kind]}${search}`;
};

type LocationState = { fromAuthFiles?: boolean } | null;

export type ModelRulesView = 'ledger' | 'graph';

/**
 * 工作台的路由状态。
 *
 * 当前 provider / 视图以本地 state 为准，并**镜像**到 `?provider=&view=`（可深链、可刷新）。
 * 不能反过来只读 search：PageTransition 在 pathname 不变时会冻结该层的 location，
 * 页面内 useSearchParams 读到的始终是进入时的那份。镜像用 replace，不产生历史记录，
 * 返回键始终回到认证文件页。
 */
export function useModelRulesRoute(kind: ModelRulesKind, isDirty: boolean) {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [provider, setProvider] = useState(() =>
    normalizeProviderKey(searchParams.get('provider') ?? '')
  );
  const [view, setViewState] = useState<ModelRulesView>(() =>
    searchParams.get('view') === 'graph' ? 'graph' : 'ledger'
  );

  // 草稿属于整页，同一路径内换 provider / 视图不会丢草稿，所以只拦截真正离开页面的导航。
  const shouldBlock = useCallback(
    ({
      currentLocation,
      nextLocation,
    }: {
      currentLocation: { pathname: string };
      nextLocation: { pathname: string };
    }) => isDirty && currentLocation.pathname !== nextLocation.pathname,
    [isDirty]
  );
  const dialog = useMemo(
    () => ({
      title: t('common.unsaved_changes_title'),
      message: t('common.unsaved_changes_message'),
      confirmText: t('common.leave'),
      cancelText: t('common.stay'),
    }),
    [t]
  );
  useUnsavedChangesGuard({ shouldBlock, dialog });

  const mirror = useCallback(
    (nextProvider: string, nextView: ModelRulesView) => {
      const next = new URLSearchParams();
      if (nextProvider) next.set('provider', nextProvider);
      if (nextView === 'graph') next.set('view', 'graph');
      setSearchParams(next, { replace: true, state: location.state });
    },
    [location.state, setSearchParams]
  );

  /** 选中 provider（空串 = 全部）并回到清单视图。 */
  const openProvider = useCallback(
    (value: string) => {
      const next = normalizeProviderKey(value);
      setProvider(next);
      setViewState('ledger');
      mirror(next, 'ledger');
    },
    [mirror]
  );

  const setView = useCallback(
    (value: ModelRulesView) => {
      setViewState(value);
      mirror(provider, value);
    },
    [mirror, provider]
  );

  const goBack = useCallback(() => {
    const state = location.state as LocationState;
    if (state?.fromAuthFiles) {
      navigate(-1);
      return;
    }
    navigate('/auth-files', { replace: true });
  }, [location.state, navigate]);

  const switchKind = useCallback(
    (nextKind: ModelRulesKind) => {
      if (nextKind === kind) return;
      navigate(buildModelRulesPath(nextKind, provider), {
        replace: true,
        state: location.state,
      });
    },
    [kind, location.state, navigate, provider]
  );

  return { provider, view, openProvider, setView, goBack, switchKind } as const;
}

export type RailSource = {
  files: readonly AuthFileItem[];
  excluded: Record<string, unknown>;
  modelAlias: Record<string, unknown>;
  /** 基线里已有配置的 provider（决定排序）。 */
  configured: readonly string[];
  /** provider → 当前草稿里的条数。 */
  counts: Record<string, number>;
  dirtyProviders: readonly string[];
  selected: string;
};

/**
 * 导轨条目：**已保存过配置**的 provider 在前，其余按预设顺序跟在后面。
 * 排序只看基线、不看草稿——否则用户一敲键盘，正在点的那一项就会从指针下面跳走。
 */
export const buildRailItems = (
  source: RailSource,
  t: TFunction,
  resolvedTheme: ResolvedTheme
): ProviderRailItem[] => {
  const candidates = new Set<string>();
  Object.keys(source.excluded).forEach((value) => candidates.add(value));
  Object.keys(source.modelAlias).forEach((value) => candidates.add(value));
  Object.keys(source.counts).forEach((value) => candidates.add(value));
  source.dirtyProviders.forEach((value) => candidates.add(value));
  if (source.selected) candidates.add(source.selected);
  source.files.forEach((file) => {
    if (typeof file.type === 'string') candidates.add(file.type);
    if (typeof file.provider === 'string') candidates.add(file.provider);
  });

  const dirty = new Set(source.dirtyProviders);
  const items = buildOAuthProviderOptions(candidates).map((key) => ({
    key,
    label: getTypeLabel(t, key),
    icon: getAuthFileIcon(key, resolvedTheme),
    count: source.counts[key] ?? 0,
    dirty: dirty.has(key),
  }));
  const saved = new Set(source.configured.map((value) => normalizeProviderKey(value)));
  const isSaved = (item: ProviderRailItem) => saved.has(item.key);
  return [...items.filter(isSaved), ...items.filter((item) => !isSaved(item))];
};
