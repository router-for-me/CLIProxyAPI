import { useState, useCallback } from 'react';
import { accountPoolApi, type MemberAccount, type LeaderAccount, type ListResponse } from '@/services/api/accountPool';

export function useMembersData() {
  const [members, setMembers] = useState<MemberAccount[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const loadMembers = useCallback(async (params?: { status?: string; search?: string; limit?: number; offset?: number }) => {
    setLoading(true);
    setError('');
    try {
      const data = await accountPoolApi.listMembers(params) as ListResponse<MemberAccount>;
      setMembers(data.items ?? []);
      setTotal(data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load members');
    } finally {
      setLoading(false);
    }
  }, []);

  return { members, total, loading, error, loadMembers, setMembers };
}

export function useLeadersData() {
  const [leaders, setLeaders] = useState<LeaderAccount[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const loadLeaders = useCallback(async (params?: { status?: string; search?: string; limit?: number; offset?: number }) => {
    setLoading(true);
    setError('');
    try {
      const data = await accountPoolApi.listLeaders(params) as ListResponse<LeaderAccount>;
      setLeaders(data.items ?? []);
      setTotal(data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load leaders');
    } finally {
      setLoading(false);
    }
  }, []);

  return { leaders, total, loading, error, loadLeaders, setLeaders };
}
