import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';
import { apiClient } from '@/services/api/client';

const pad2 = (value: number) => String(value).padStart(2, '0');
export const date = (value: string) => {
  if (!value) return '—';
  const time = new Date(value);
  if (Number.isNaN(time.getTime())) return '—';
  return `${time.getFullYear()}-${pad2(time.getMonth() + 1)}-${pad2(time.getDate())} ${pad2(time.getHours())}:${pad2(time.getMinutes())}:${pad2(time.getSeconds())}`;
};
export const compactDate = (value: string) => {
  if (!value) return '—';
  const time = new Date(value);
  if (Number.isNaN(time.getTime())) return '—';
  return `${pad2(time.getMonth() + 1)}-${pad2(time.getDate())} ${pad2(time.getHours())}:${pad2(time.getMinutes())}:${pad2(time.getSeconds())}`;
};
export const number = (value: number | undefined) => Number(value || 0).toLocaleString();
export const duration = (ms: number) =>
  ms >= 1000 ? `${(ms / 1000).toFixed(1)}s` : `${number(ms)}ms`;

export type SharingDataLoader<T> = (signal: AbortSignal) => Promise<T>;

// Last successful result per cacheKey. Returning to a page (or paging back)
// paints the previous figures at once while a fresh request runs, so a slow
// uplink shows content instead of skeletons. Entries die with the connection
// (login switch, key change) and are bounded to the most recent few.
const CACHE_LIMIT = 48;
const resultCache = new Map<string, { data: unknown; connection: number }>();

const readCache = <T>(cacheKey: string | undefined): T | null => {
  if (!cacheKey) return null;
  const hit = resultCache.get(cacheKey);
  if (!hit || hit.connection !== apiClient.getConnectionRevision()) return null;
  return hit.data as T;
};

export const writeSharingDataCache = (
  cacheKey: string | undefined,
  data: unknown,
  connection = apiClient.getConnectionRevision()
) => {
  if (!cacheKey || connection !== apiClient.getConnectionRevision()) return;
  resultCache.delete(cacheKey);
  resultCache.set(cacheKey, { data, connection });
  while (resultCache.size > CACHE_LIMIT) {
    const oldest = resultCache.keys().next().value;
    if (oldest === undefined) break;
    resultCache.delete(oldest);
  }
};

export function clearSharingDataCache() {
  resultCache.clear();
}

export function useSharingData<T>(
  loader: SharingDataLoader<T>,
  {
    keepPreviousData = true,
    headerRefresh = true,
    dataKey,
    cacheKey,
  }: {
    keepPreviousData?: boolean;
    headerRefresh?: boolean;
    /** When set, previous data stays visible while the loader identity changes
     * but this key does not (e.g. pagination within the same filter scope);
     * changing the key (scope/filter/account) drops the stale data. */
    dataKey?: string;
    /** Identifies the exact request (account, filters, page). A cached result
     * for it is shown immediately and revalidated in the background. */
    cacheKey?: string;
  } = {}
) {
  const [state, setState] = useState<{
    loader: SharingDataLoader<T>;
    dataKey: string | undefined;
    data: T | null;
    error: string;
    loading: boolean;
  }>(() => ({ loader, dataKey, data: readCache<T>(cacheKey), error: '', loading: true }));
  const revision = useRef(0);
  const inflight = useRef<AbortController | null>(null);
  const refresh = useCallback(async () => {
    const id = ++revision.current;
    const connection = apiClient.getConnectionRevision();
    // Superseded requests (repeat refresh, dependency change, unmount) are
    // aborted so stale traffic never overwrites or errors the live view.
    inflight.current?.abort();
    const controller = new AbortController();
    inflight.current = controller;
    const isCurrent = () =>
      id === revision.current && connection === apiClient.getConnectionRevision();
    const cached = readCache<T>(cacheKey);
    setState((current) => ({
      loader,
      dataKey,
      data:
        cached ??
        (keepPreviousData ||
        current.loader === loader ||
        (dataKey !== undefined && current.dataKey === dataKey)
          ? current.data
          : null),
      error: '',
      loading: true,
    }));
    try {
      const data = await loader(controller.signal);
      if (isCurrent()) {
        writeSharingDataCache(cacheKey, data, connection);
        setState({ loader, dataKey, data, error: '', loading: false });
      }
    } catch (error) {
      // Aborted loaders are always superseded or unmounted; stay silent.
      if (controller.signal.aborted || !isCurrent()) return;
      setState((current) => ({
        ...current,
        error: error instanceof Error ? error.message : String(error),
        loading: false,
      }));
    } finally {
      if (inflight.current === controller) inflight.current = null;
    }
  }, [loader, keepPreviousData, dataKey, cacheKey]);
  useEffect(() => {
    const scope = revision;
    const pending = inflight;
    void refresh();
    return () => {
      scope.current++;
      pending.current?.abort();
    };
  }, [refresh]);
  useHeaderRefresh(refresh, headerRefresh);
  const matchesLoader = state.loader === loader;
  const matchesKey = dataKey !== undefined && state.dataKey === dataKey;
  const matches = keepPreviousData || matchesLoader || matchesKey;
  return {
    data: matches ? state.data : readCache<T>(cacheKey),
    loading: !matchesLoader || state.loading,
    error: matchesLoader ? state.error : '',
    refresh,
  };
}

export function useSharingActions(refresh: () => Promise<void>) {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const run = async (operation: () => Promise<unknown>) => {
    try {
      await operation();
      await refresh();
      showNotification(t('sharing.saved'), 'success');
      return true;
    } catch (error) {
      showNotification(error instanceof Error ? error.message : String(error), 'error');
      return false;
    }
  };
  const confirm = (message: string, operation: () => Promise<unknown>) =>
    showConfirmation({
      title: t('common.confirm'),
      message,
      variant: 'danger',
      onConfirm: async () => {
        await run(operation);
      },
    });
  const copy = async (value: string) => {
    if (await copyToClipboard(value)) showNotification(t('sharing.copied'), 'success');
    else showNotification(t('sharing.copy_failed'), 'error');
  };
  return { run, confirm, copy };
}
