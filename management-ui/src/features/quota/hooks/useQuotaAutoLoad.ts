/**
 * Auto-load quota for credentials whose cache is still idle.
 *
 * The management panel previously required an explicit click per card. When the
 * backend has no quota data yet the card now issues the request automatically,
 * once per credential per session. Entries that already hold data (or are being
 * loaded) are skipped, and the batch loader keeps its own request de-duplication
 * and stale-session guards.
 */

import { useEffect, useRef } from 'react';
import { useQuotaStore } from '@/stores';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import { QUOTA_ADAPTERS } from '../providers';
import type { QuotaFileEntry } from '../logic';

export function useQuotaAutoLoad(
  entries: QuotaFileEntry[],
  disabled: boolean,
  loadQuota: (targets: QuotaFileEntry[]) => Promise<void>
) {
  const attempted = useRef(new Set<string>());
  const session = useQuotaStore((state) => state.cacheGeneration);
  const fileGenerations = useQuotaStore((state) => state.fileGenerations);

  useEffect(() => {
    if (disabled) return;

    const targets = entries.filter(({ type, file }) => {
      const key = JSON.stringify([
        session,
        fileGenerations[file.name] ?? 0,
        file.name,
        file.authIndex,
      ]);
      if (attempted.current.has(key)) return false;
      attempted.current.add(key);

      const adapter = QUOTA_ADAPTERS[type];
      const state = adapter.storeSelector(useQuotaStore.getState())[getQuotaCacheKey(file)];
      return state?.status !== 'loading' && state?.status !== 'success';
    });

    if (targets.length > 0) void loadQuota(targets);
  }, [disabled, entries, fileGenerations, loadQuota, session]);
}
