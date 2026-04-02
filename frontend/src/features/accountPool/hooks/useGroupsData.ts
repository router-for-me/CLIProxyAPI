import { useState, useCallback } from 'react';
import { accountPoolApi, type GroupRun, type GroupRunJSON, type ListResponse } from '@/services/api/accountPool';

export function useGroupRunsData() {
  const [groupRuns, setGroupRuns] = useState<GroupRun[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [expandedRun, setExpandedRun] = useState<GroupRunJSON | null>(null);
  const [expandedRunId, setExpandedRunId] = useState<number | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);

  const loadGroupRuns = useCallback(async (params?: { date?: string; group_id?: number; status?: string; limit?: number; offset?: number }) => {
    setLoading(true);
    setError('');
    try {
      const data = await accountPoolApi.listGroupRuns(params) as ListResponse<GroupRun>;
      setGroupRuns(data.items ?? []);
      setTotal(data.total);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to load group runs');
    } finally {
      setLoading(false);
    }
  }, []);

  const toggleExpand = useCallback(async (id: number) => {
    if (expandedRunId === id) {
      setExpandedRunId(null);
      setExpandedRun(null);
      return;
    }
    setExpandedRunId(id);
    setDetailLoading(true);
    try {
      const data = await accountPoolApi.getGroupRunJSON(id) as GroupRunJSON;
      setExpandedRun(data);
    } catch {
      setExpandedRun(null);
    } finally {
      setDetailLoading(false);
    }
  }, [expandedRunId]);

  return { groupRuns, total, loading, error, loadGroupRuns, expandedRun, expandedRunId, detailLoading, toggleExpand };
}
