import { useMemo } from 'react';
import type { AuthFileItem } from '@/types';
import { calculateStatusBarData, normalizeAuthIndex, type UsageDetail } from '@/utils/usage';

export type AuthFileStatusBarData = ReturnType<typeof calculateStatusBarData>;

export function useAuthFilesStatusBarCache(files: AuthFileItem[], usageDetails: UsageDetail[]) {
  return useMemo(() => {
    const cache = new Map<string, AuthFileStatusBarData>();

    files.forEach((file) => {
      const rawAuthIndex = file['auth_index'] ?? file.authIndex;
      const authIndexKey = normalizeAuthIndex(rawAuthIndex);

      if (authIndexKey) {
        const filteredDetails = usageDetails.filter((detail) => {
          const detailAuthIndex = normalizeAuthIndex(detail.auth_index);
          return detailAuthIndex !== null && detailAuthIndex === authIndexKey;
        });
        cache.set(authIndexKey, calculateStatusBarData(filteredDetails));
      }
    });

    return cache;
  }, [files, usageDetails]);
}
