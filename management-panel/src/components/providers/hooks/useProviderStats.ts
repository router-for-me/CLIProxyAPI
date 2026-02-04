import { useCallback, useRef, useState } from 'react';
import { useInterval } from '@/hooks/useInterval';
import { usageApi } from '@/services/api';
import { collectUsageDetails, type KeyStats, type UsageDetail } from '@/utils/usage';

const EMPTY_STATS: KeyStats = { bySource: {}, byAuthIndex: {} };

export const useProviderStats = () => {
  const [keyStats, setKeyStats] = useState<KeyStats>(EMPTY_STATS);
  const [usageDetails, setUsageDetails] = useState<UsageDetail[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const loadingRef = useRef(false);

  // 加载 key 统计和 usage 明细（API 层已有60秒超时）
  const loadKeyStats = useCallback(async () => {
    if (loadingRef.current) return;
    loadingRef.current = true;
    setIsLoading(true);
    try {
      const usageResponse = await usageApi.getUsage();
      const usageData = usageResponse?.usage ?? usageResponse;
      const stats = await usageApi.getKeyStats(usageData);
      setKeyStats(stats);
      setUsageDetails(collectUsageDetails(usageData));
    } catch {
      // 静默失败
    } finally {
      loadingRef.current = false;
      setIsLoading(false);
    }
  }, []);

  // 定时刷新状态数据（每240秒）
  useInterval(loadKeyStats, 240_000);

  return { keyStats, usageDetails, loadKeyStats, isLoading };
};
