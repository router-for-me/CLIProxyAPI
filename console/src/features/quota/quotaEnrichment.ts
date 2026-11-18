import type { TFunction } from 'i18next';
import type { AuthFileItem } from '@/types';
import {
  captureQuotaCacheGeneration,
  commitIfQuotaCacheCurrent,
  useQuotaStore,
} from '@/stores/useQuotaStore';
import { getQuotaCacheKey } from '@/utils/quota/identity';
import type { QuotaAdapter, QuotaCardState, QuotaMapUpdater } from './providers';

/** Optional metadata must neither delay quota rendering nor replace a newer result. */
export async function enrichQuotaInBackground(
  adapter: QuotaAdapter,
  file: AuthFileItem,
  data: unknown,
  expectedState: QuotaCardState,
  t: TFunction
): Promise<void> {
  if (!adapter.enrichQuota) return;
  const cacheKey = getQuotaCacheKey(file);
  const currentState = () => adapter.storeSelector(useQuotaStore.getState())[cacheKey];
  if (currentState() !== expectedState) return;
  const generation = captureQuotaCacheGeneration(file.name);

  try {
    const enriched = await adapter.enrichQuota(file, data, t);
    if (enriched === data) return;
    commitIfQuotaCacheCurrent(generation, () => {
      const setQuota = useQuotaStore.getState()[adapter.storeSetter] as QuotaMapUpdater;
      setQuota((prev) => {
        if (prev[cacheKey] !== expectedState) return prev;
        return { ...prev, [cacheKey]: adapter.buildSuccessState(enriched) };
      });
    });
  } catch {
    // Keep the successful quota and its original plan fallback on optional failures.
  }
}
