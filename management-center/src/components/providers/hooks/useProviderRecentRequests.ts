import { useCallback, useEffect, useMemo, useState } from 'react';
import { useInterval } from '@/hooks/useInterval';
import { apiKeyUsageApi } from '@/services/api';
import { useAuthStore } from '@/stores';
import {
  normalizeRecentRequestUsageEntry,
  type ApiKeyUsageResponse,
  type RecentRequestUsageEntry,
} from '@/utils/recentRequests';

const PROVIDER_RECENT_REQUESTS_STALE_TIME_MS = 240_000;

export type ProviderRecentRequests = Map<string, Map<string, RecentRequestUsageEntry>>;

export type UseProviderRecentRequestsOptions = {
  enabled?: boolean;
};

const EMPTY_USAGE_BY_PROVIDER: ProviderRecentRequests = new Map();

type ProviderRecentRequestsCache = {
  cachedUsageByProvider: ProviderRecentRequests;
  cachedAt: number;
  inFlightRequest: Promise<ProviderRecentRequests> | null;
};

const createProviderRecentRequestsCache = (): ProviderRecentRequestsCache => ({
  cachedUsageByProvider: EMPTY_USAGE_BY_PROVIDER,
  cachedAt: 0,
  inFlightRequest: null,
});

export const createProviderRecentRequestsCacheController = () => {
  let currentApiBase = '';
  let currentManagementKey = '';
  let currentCache = createProviderRecentRequestsCache();

  return {
    forScope(apiBase: string, managementKey: string): ProviderRecentRequestsCache {
      if (apiBase !== currentApiBase || managementKey !== currentManagementKey) {
        currentApiBase = apiBase;
        currentManagementKey = managementKey;
        currentCache = createProviderRecentRequestsCache();
      }
      return currentCache;
    },
  };
};

const providerRecentRequestsCacheController = createProviderRecentRequestsCacheController();

const normalizeProviderKey = (value: unknown): string =>
  String(value ?? '')
    .trim()
    .toLowerCase();

const normalizeApiKeyUsageResponse = (payload: ApiKeyUsageResponse): ProviderRecentRequests => {
  if (!payload || typeof payload !== 'object' || Array.isArray(payload)) {
    return EMPTY_USAGE_BY_PROVIDER;
  }

  const usageByProvider: ProviderRecentRequests = new Map();

  Object.entries(payload).forEach(([provider, entries]) => {
    const providerKey = normalizeProviderKey(provider);
    if (!providerKey || !entries || typeof entries !== 'object' || Array.isArray(entries)) {
      return;
    }

    const usageByCompositeKey = new Map<string, RecentRequestUsageEntry>();
    Object.entries(entries).forEach(([compositeKey, entry]) => {
      usageByCompositeKey.set(compositeKey, normalizeRecentRequestUsageEntry(entry));
    });

    usageByProvider.set(providerKey, usageByCompositeKey);
  });

  return usageByProvider;
};

const fetchProviderRecentRequests = async (
  cache: ProviderRecentRequestsCache
): Promise<ProviderRecentRequests> => {
  if (!cache.inFlightRequest) {
    const request = apiKeyUsageApi
      .getUsage()
      .then((payload) => {
        const normalized = normalizeApiKeyUsageResponse(payload);
        cache.cachedUsageByProvider = normalized;
        cache.cachedAt = Date.now();
        return normalized;
      })
      .finally(() => {
        if (cache.inFlightRequest === request) {
          cache.inFlightRequest = null;
        }
      });
    cache.inFlightRequest = request;
  }

  return cache.inFlightRequest;
};

export function useProviderRecentRequests(options: UseProviderRecentRequestsOptions = {}) {
  const enabled = options.enabled ?? true;
  const apiBase = useAuthStore((state) => state.apiBase);
  const managementKey = useAuthStore((state) => state.managementKey);
  const cache = useMemo(
    () => providerRecentRequestsCacheController.forScope(apiBase, managementKey),
    [apiBase, managementKey]
  );
  const [usageState, setUsageState] = useState(() => ({
    cache,
    value: cache.cachedUsageByProvider,
  }));
  const [loadingState, setLoadingState] = useState(() => ({ cache, value: false }));

  const setUsageForCurrentScope = useCallback(
    (value: ProviderRecentRequests) => setUsageState({ cache, value }),
    [cache]
  );

  const setLoadingForCurrentScope = useCallback(
    (value: boolean) => setLoadingState({ cache, value }),
    [cache]
  );

  const loadRecentRequests = useCallback(
    async (loadOptions: { force?: boolean } = {}) => {
      if (!enabled) {
        return EMPTY_USAGE_BY_PROVIDER;
      }

      const hasFreshCache =
        cache.cachedAt > 0 &&
        Date.now() - cache.cachedAt < PROVIDER_RECENT_REQUESTS_STALE_TIME_MS;

      if (!loadOptions.force && hasFreshCache) {
        setUsageForCurrentScope(cache.cachedUsageByProvider);
        return cache.cachedUsageByProvider;
      }

      setLoadingForCurrentScope(true);
      try {
        const nextUsage = await fetchProviderRecentRequests(cache);
        setUsageForCurrentScope(nextUsage);
        return nextUsage;
      } catch {
        if (cache.cachedAt > 0) {
          setUsageForCurrentScope(cache.cachedUsageByProvider);
        }
        return cache.cachedUsageByProvider;
      } finally {
        setLoadingForCurrentScope(false);
      }
    },
    [cache, enabled, setLoadingForCurrentScope, setUsageForCurrentScope]
  );

  const refreshRecentRequests = useCallback(
    async () => loadRecentRequests({ force: true }),
    [loadRecentRequests]
  );

  useEffect(() => {
    if (!enabled) {
      setUsageForCurrentScope(EMPTY_USAGE_BY_PROVIDER);
      return;
    }
    void loadRecentRequests().catch(() => {});
  }, [enabled, loadRecentRequests, setUsageForCurrentScope]);

  useInterval(
    () => {
      void refreshRecentRequests().catch(() => {});
    },
    enabled ? PROVIDER_RECENT_REQUESTS_STALE_TIME_MS : null
  );

  const usageByProvider =
    usageState.cache === cache ? usageState.value : cache.cachedUsageByProvider;
  const isLoading =
    loadingState.cache === cache ? loadingState.value : cache.inFlightRequest !== null;

  return {
    usageByProvider: enabled ? usageByProvider : EMPTY_USAGE_BY_PROVIDER,
    isLoading: enabled ? isLoading : false,
    loadRecentRequests,
    refreshRecentRequests,
  };
}
