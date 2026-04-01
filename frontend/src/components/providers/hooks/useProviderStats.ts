import { useCallback } from 'react';
import { useInterval } from '@/hooks/useInterval';
import { USAGE_STATS_STALE_TIME_MS, useUsageStatsStore } from '@/stores';

export const useProviderStats = () => {
  const keyStats = useUsageStatsStore((state) => state.keyStats);
  const usageDetails = useUsageStatsStore((state) => state.usageDetails);
  const isLoading = useUsageStatsStore((state) => state.loading);
  const loadUsageStats = useUsageStatsStore((state) => state.loadUsageStats);

  // 首次进入页面优先复用缓存，避免跨页面重复拉取 /usage。
  const loadKeyStats = useCallback(async () => {
    await loadUsageStats({ staleTimeMs: USAGE_STATS_STALE_TIME_MS });
  }, [loadUsageStats]);

  // 定时器触发时强制刷新共享 usage。
  const refreshKeyStats = useCallback(async () => {
    await loadUsageStats({ force: true, staleTimeMs: USAGE_STATS_STALE_TIME_MS });
  }, [loadUsageStats]);

  useInterval(() => {
    void refreshKeyStats().catch(() => {});
  }, 240_000);

  return { keyStats, usageDetails, loadKeyStats, refreshKeyStats, isLoading };
};
