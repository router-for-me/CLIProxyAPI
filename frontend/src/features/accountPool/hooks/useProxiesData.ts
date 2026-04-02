import { useState, useCallback } from 'react';
import { accountPoolApi, type PoolProxy, type ListResponse } from '@/services/api/accountPool';

export function useProxiesData() {
  const [proxies, setProxies] = useState<PoolProxy[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const loadProxies = useCallback(async (params?: { type?: string; status?: string; limit?: number; offset?: number }) => {
    setLoading(true);
    setError('');
    try {
      const data = await accountPoolApi.listProxies(params) as ListResponse<PoolProxy>;
      setProxies(data.items ?? []);
      setTotal(data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load proxies');
    } finally {
      setLoading(false);
    }
  }, []);

  return { proxies, total, loading, error, loadProxies, setProxies };
}
