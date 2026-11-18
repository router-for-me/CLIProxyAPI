import { useEffect, useState } from 'react';
import { cacheStatsApi, type CacheStats } from '@/services/api/cacheStats';

const POLL_MS = 30_000;

/**
 * Poll papa-proxy's per-credential cache counters while the ledger is open.
 * Older backends without the endpoint simply yield null.
 */
export function useCacheStats(enabled: boolean): CacheStats | null {
  const [stats, setStats] = useState<CacheStats | null>(null);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    const load = () => {
      cacheStatsApi
        .get()
        .then((next) => {
          if (!cancelled) setStats(next);
        })
        .catch(() => {
          if (!cancelled) setStats(null);
        });
    };
    load();
    const timer = window.setInterval(load, POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [enabled]);

  return stats;
}
