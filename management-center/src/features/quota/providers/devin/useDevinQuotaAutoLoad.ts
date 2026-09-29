import { useEffect, useRef } from 'react';
import { useQuotaStore } from '@/stores/useQuotaStore';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import type { QuotaFileEntry } from '../../logic';

/** Devin's active management query runs once per visible credential per visit.
 * Other providers retain their existing click-to-load behavior. No polling.
 */
export function useDevinQuotaAutoLoad(
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
      if (type !== 'devin') return false;
      const key = JSON.stringify([
        session,
        fileGenerations[file.name] ?? 0,
        file.name,
        file.authIndex,
      ]);
      if (attempted.current.has(key)) return false;
      attempted.current.add(key);
      // An explicit refresh already started in this effect cycle counts too.
      return useQuotaStore.getState().devinQuota[getQuotaCacheKey(file)]?.status !== 'loading';
    });
    if (targets.length > 0) void loadQuota(targets);
  }, [disabled, entries, fileGenerations, loadQuota, session]);
}
