import { useState, useCallback } from 'react';
import { accountPoolApi, type AccountGroup, type ListResponse } from '@/services/api/accountPool';

export function useGroupsData() {
  const [groups, setGroups] = useState<AccountGroup[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const loadGroups = useCallback(async (params?: { group_id?: string; leader_email?: string; limit?: number; offset?: number }) => {
    setLoading(true);
    setError('');
    try {
      const data = await accountPoolApi.listGroups(params) as ListResponse<AccountGroup>;
      setGroups(data.items ?? []);
      setTotal(data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load groups');
    } finally {
      setLoading(false);
    }
  }, []);

  return { groups, total, loading, error, loadGroups, setGroups };
}
