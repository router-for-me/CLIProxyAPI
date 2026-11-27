import { useMemo } from 'react';
import type { AuthFileItem } from '@/types';
import {
  normalizeRecentRequestAuthIndex,
  normalizeRecentRequestBuckets,
  statusBarDataFromRecentRequests,
} from '@/utils/recentRequests';

export type AuthFileStatusBarData = ReturnType<typeof statusBarDataFromRecentRequests>;

export function useAuthFilesStatusBarCache(files: AuthFileItem[]) {
  return useMemo(() => {
    const cache = new Map<string, AuthFileStatusBarData>();

    files.forEach((file) => {
      const rawAuthIndex = file['auth_index'] ?? file.authIndex;
      const authIndexKey = normalizeRecentRequestAuthIndex(rawAuthIndex);
      if (!authIndexKey) return;

      cache.set(
        authIndexKey,
        statusBarDataFromRecentRequests(
          normalizeRecentRequestBuckets(file.recent_requests ?? file.recentRequests)
        )
      );
    });

    return cache;
  }, [files]);
}
